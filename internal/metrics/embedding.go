package metrics

import "github.com/prometheus/client_golang/prometheus"

// Embedding Prometheus metrics.
var (
	EmbeddingRequestsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "vecdex",
			Name:      "embedding_requests_total",
			Help:      "Total number of embedding requests",
		},
		[]string{"provider", "model", "status"},
	)

	EmbeddingRequestDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: "vecdex",
			Name:      "embedding_request_duration_seconds",
			Help:      "Embedding request duration in seconds",
			Buckets:   []float64{0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10},
		},
		[]string{"provider", "model"},
	)

	EmbeddingTokensTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "vecdex",
			Name:      "embedding_tokens_total",
			Help:      "Total embedding tokens consumed",
		},
		[]string{"provider", "model", "type"},
	)

	EmbeddingErrorsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "vecdex",
			Name:      "embedding_errors_total",
			Help:      "Total embedding errors",
		},
		[]string{"provider", "model", "error_type"},
	)

	EmbeddingBudgetTokensRemaining = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: "vecdex",
			Name:      "embedding_budget_tokens_remaining",
			Help:      "Remaining token budget",
		},
		[]string{"provider", "period"},
	)

	EmbeddingCacheTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "vecdex",
			Name:      "embedding_cache_total",
			Help:      "Embedding cache hits and misses",
		},
		[]string{"result"}, // "hit" / "miss"
	)
)

var embMetricsRegistered bool

// RegisterEmbeddingMetrics registers Prometheus embedding metrics. Must be called once from main.
func RegisterEmbeddingMetrics() {
	if embMetricsRegistered {
		return
	}
	prometheus.MustRegister(EmbeddingRequestsTotal)
	prometheus.MustRegister(EmbeddingRequestDuration)
	prometheus.MustRegister(EmbeddingTokensTotal)
	prometheus.MustRegister(EmbeddingErrorsTotal)
	prometheus.MustRegister(EmbeddingBudgetTokensRemaining)
	prometheus.MustRegister(EmbeddingCacheTotal)
	embMetricsRegistered = true
}
