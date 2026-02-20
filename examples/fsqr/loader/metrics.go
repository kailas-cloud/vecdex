// Prometheus метрики для FSQ loader.
// Прогресс загрузки, batch latency, Valkey memory, index sizes.
package main

import (
	"context"
	"log"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/redis/rueidis"
)

// loaderMetrics — все Prometheus метрики loader'а.
type loaderMetrics struct {
	rowsProcessed *prometheus.CounterVec
	rowsFailed    *prometheus.CounterVec
	batchesTotal  *prometheus.CounterVec
	batchDuration *prometheus.HistogramVec

	downloadBytes prometheus.Counter

	cursorPosition prometheus.Gauge

	valkeyMemory *prometheus.GaugeVec
	indexSize    *prometheus.GaugeVec
	indexDocs    *prometheus.GaugeVec
}

func newLoaderMetrics(reg prometheus.Registerer) *loaderMetrics {
	m := &loaderMetrics{
		rowsProcessed: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "fsqr_loader",
			Name:      "rows_processed_total",
			Help:      "Total rows successfully processed",
		}, []string{"collection"}),

		rowsFailed: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "fsqr_loader",
			Name:      "rows_failed_total",
			Help:      "Total rows failed",
		}, []string{"collection", "reason"}),

		batchesTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "fsqr_loader",
			Name:      "batches_total",
			Help:      "Total batches sent",
		}, []string{"collection"}),

		batchDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "fsqr_loader",
			Name:      "batch_duration_seconds",
			Help:      "Batch upsert duration",
			Buckets:   []float64{0.01, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10},
		}, []string{"collection"}),

		downloadBytes: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "fsqr_loader",
			Name:      "download_bytes_total",
			Help:      "Total bytes downloaded from HuggingFace",
		}),

		cursorPosition: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "fsqr_loader",
			Name:      "cursor_position",
			Help:      "Current cursor row offset",
		}),

		valkeyMemory: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: "fsqr_loader",
			Name:      "valkey_memory_bytes",
			Help:      "Valkey memory usage",
		}, []string{"type"}),

		indexSize: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: "fsqr_loader",
			Name:      "index_size_bytes",
			Help:      "FT.INDEX component sizes",
		}, []string{"collection", "component"}),

		indexDocs: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: "fsqr_loader",
			Name:      "index_docs_total",
			Help:      "Number of documents in index",
		}, []string{"collection"}),
	}

	reg.MustRegister(
		m.rowsProcessed, m.rowsFailed,
		m.batchesTotal, m.batchDuration,
		m.downloadBytes, m.cursorPosition,
		m.valkeyMemory, m.indexSize, m.indexDocs,
	)

	return m
}

// serveMetrics запускает HTTP сервер для Prometheus scrape.
func serveMetrics(port string) *http.Server {
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())

	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		log.Printf("metrics server on :%s/metrics", port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("metrics server error: %v", err)
		}
	}()

	return srv
}

// valkeyPoller периодически опрашивает Valkey для метрик памяти и индекса.
type valkeyPoller struct {
	client      rueidis.Client
	metrics     *loaderMetrics
	collections []string
	interval    time.Duration
	prefix      string // key prefix, default "vecdex:"
}

// Start запускает фоновую горутину. Останавливается по ctx.Done().
func (p *valkeyPoller) Start(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(p.interval)
		defer ticker.Stop()

		// Первый poll сразу.
		p.poll(ctx)

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				p.poll(ctx)
			}
		}
	}()
}

func (p *valkeyPoller) poll(ctx context.Context) {
	p.pollMemory(ctx)
	for _, coll := range p.collections {
		p.pollIndex(ctx, coll)
	}
}

func (p *valkeyPoller) pollMemory(ctx context.Context) {
	cmd := p.client.B().Info().Section("memory").Build()
	resp := p.client.Do(ctx, cmd)
	if resp.Error() != nil {
		return
	}
	text, _ := resp.ToString()
	for _, kv := range parseInfoFields(text) {
		switch kv.key {
		case "used_memory":
			p.metrics.valkeyMemory.WithLabelValues("used").Set(kv.val)
		case "used_memory_peak":
			p.metrics.valkeyMemory.WithLabelValues("peak").Set(kv.val)
		case "used_memory_rss":
			p.metrics.valkeyMemory.WithLabelValues("rss").Set(kv.val)
		}
	}
}

func (p *valkeyPoller) pollIndex(ctx context.Context, collection string) {
	indexName := p.prefix + collection + ":idx"
	cmd := p.client.B().Arbitrary("FT.INFO").Args(indexName).Build()
	resp := p.client.Do(ctx, cmd)
	if resp.Error() != nil {
		return
	}

	arr, err := resp.ToArray()
	if err != nil {
		return
	}

	// FT.INFO returns alternating key-value pairs.
	for i := 0; i+1 < len(arr); i += 2 {
		key, _ := arr[i].ToString()
		switch key {
		case "num_docs":
			val, _ := arr[i+1].AsFloat64()
			p.metrics.indexDocs.WithLabelValues(collection).Set(val)
		case "inverted_sz_mb":
			val, _ := arr[i+1].AsFloat64()
			p.metrics.indexSize.WithLabelValues(collection, "inverted").Set(val * 1024 * 1024)
		case "doc_table_size_mb":
			val, _ := arr[i+1].AsFloat64()
			p.metrics.indexSize.WithLabelValues(collection, "data").Set(val * 1024 * 1024)
		case "vector_index_sz_mb":
			val, _ := arr[i+1].AsFloat64()
			p.metrics.indexSize.WithLabelValues(collection, "vector").Set(val * 1024 * 1024)
		case "geo_index_sz_mb":
			val, _ := arr[i+1].AsFloat64()
			p.metrics.indexSize.WithLabelValues(collection, "geo").Set(val * 1024 * 1024)
		}
	}
}

type infoField struct {
	key string
	val float64
}

func parseInfoFields(text string) []infoField {
	var fields []infoField
	var line []byte
	for i := 0; i < len(text); i++ {
		if text[i] == '\n' {
			fields = appendInfoField(fields, string(line))
			line = line[:0]
		} else if text[i] != '\r' {
			line = append(line, text[i])
		}
	}
	if len(line) > 0 {
		fields = appendInfoField(fields, string(line))
	}
	return fields
}

func appendInfoField(fields []infoField, line string) []infoField {
	for i := 0; i < len(line); i++ {
		if line[i] == ':' {
			key := line[:i]
			val := parseFloat(line[i+1:])
			return append(fields, infoField{key: key, val: val})
		}
	}
	return fields
}

func parseFloat(s string) float64 {
	var result float64
	var decimal float64
	inDecimal := false
	for _, c := range s {
		if c >= '0' && c <= '9' {
			if inDecimal {
				decimal /= 10
				result += float64(c-'0') * decimal
			} else {
				result = result*10 + float64(c-'0')
			}
		} else if c == '.' {
			inDecimal = true
			decimal = 1
		}
	}
	return result
}
