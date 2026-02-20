// FSQ OS Places ingest pipeline для vecdex.
// Скачивает parquet с HuggingFace, загружает venues (geo) и categories (text)
// в Valkey через vecdex SDK. Поддерживает resume, многопоточность, Prometheus метрики.
//
// Использование:
//
//	fsqr-loader -data-dir /data -max-rows 1000000 -workers 8
//
// Env vars:
//
//	VALKEY_ADDR     — адрес Valkey (default: localhost:6379)
//	VALKEY_PASSWORD — пароль Valkey
//	NEBIUS_API_KEY  — API ключ Nebius Inference (для эмбеддингов категорий)
//	HF_TOKEN        — HuggingFace API token (gated dataset)
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/redis/rueidis"

	vecdex "github.com/kailas-cloud/vecdex/pkg/sdk"
)

func main() {
	cfg := parseFlags()

	ctx, cancel := signal.NotifyContext(
		context.Background(), syscall.SIGTERM, syscall.SIGINT,
	)
	defer cancel()

	if err := run(ctx, cfg); err != nil {
		cancel()
		log.Fatal(err)
	}
}

type config struct {
	dataDir        string
	maxRows        int
	maxFiles       int
	workers        int
	batchSize      int
	metricsPort    string
	cursorInterval int
	reset          bool
}

func parseFlags() config {
	cfg := config{}
	flag.StringVar(&cfg.dataDir, "data-dir", "/data", "directory for parquet files and cursor")
	flag.IntVar(&cfg.maxRows, "max-rows", 1_000_000, "max venues to load (0=unlimited)")
	flag.IntVar(&cfg.maxFiles, "max-files", 2, "max parquet files to download (0=all)")
	flag.IntVar(&cfg.workers, "workers", 8, "number of parallel upsert workers")
	flag.IntVar(&cfg.batchSize, "batch-size", 100, "documents per batch upsert")
	flag.StringVar(&cfg.metricsPort, "metrics-port", "9090", "Prometheus metrics port")
	flag.IntVar(&cfg.cursorInterval, "cursor-interval", 10000, "save cursor every N rows")
	flag.BoolVar(&cfg.reset, "reset", false, "reset cursor and start from scratch")
	flag.Parse()
	return cfg
}

func run(ctx context.Context, cfg config) error {
	start := time.Now()

	reg := prometheus.NewRegistry()
	metrics := newLoaderMetrics(reg)
	metricsSrv := serveMetrics(cfg.metricsPort, reg)
	defer func() {
		shutCtx, shutCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutCancel()
		_ = metricsSrv.Shutdown(shutCtx)
	}()

	cursor, err := newCursorTracker(cfg.dataDir, cfg.cursorInterval)
	if err != nil {
		return fmt.Errorf("cursor: %w", err)
	}
	if cfg.reset {
		cursor.Reset()
		log.Println("cursor reset, starting from scratch")
	}

	if err := stageDownload(cfg); err != nil {
		return err
	}

	client, err := connectVecdex(ctx)
	if err != nil {
		return err
	}
	defer client.Close()

	startValkeyPoller(ctx, metrics)

	cats, err := stageCategories(ctx, client, cfg, cursor, metrics)
	if err != nil {
		return err
	}

	result, err := stageVenues(ctx, client, cfg, cursor, cats, metrics)
	if err != nil {
		return err
	}

	stageReport(ctx, client, cats, result, start)
	cursor.Done()

	return nil
}

func stageDownload(cfg config) error {
	log.Println("=== Stage 1: Download ===")
	dl := newDownloader(os.Getenv("HF_TOKEN"), cfg.dataDir, nil)

	if err := dl.DownloadPlaces(cfg.maxFiles); err != nil {
		return fmt.Errorf("download places: %w", err)
	}
	if _, err := dl.DownloadCategories(); err != nil {
		return fmt.Errorf("download categories: %w", err)
	}
	return nil
}

func connectVecdex(ctx context.Context) (*vecdex.Client, error) {
	addr := env("VALKEY_ADDR", "localhost:6379")
	password := env("VALKEY_PASSWORD", "")
	apiKey := os.Getenv("NEBIUS_API_KEY")

	opts := []vecdex.Option{vecdex.WithValkey(addr, password)}
	if apiKey != "" {
		opts = append(opts,
			vecdex.WithEmbedder(NewNebiusEmbedder(apiKey)),
			vecdex.WithVectorDimensions(4096),
		)
	}

	client, err := vecdex.New(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("vecdex connect: %w", err)
	}
	return client, nil
}

func startValkeyPoller(ctx context.Context, metrics *loaderMetrics) {
	addr := env("VALKEY_ADDR", "localhost:6379")
	password := env("VALKEY_PASSWORD", "")

	valkeyClient, err := rueidis.NewClient(rueidis.ClientOption{
		InitAddress: []string{addr},
		Password:    password,
	})
	if err != nil {
		log.Printf("warning: cannot connect rueidis for metrics: %v", err)
		return
	}

	poller := &valkeyPoller{
		client:      valkeyClient,
		metrics:     metrics,
		collections: []string{"fsqr-venues", "fsqr-categories"},
		interval:    30 * time.Second,
		prefix:      "vecdex:",
	}
	poller.Start(ctx)
}

func stageCategories(
	ctx context.Context,
	client *vecdex.Client,
	cfg config,
	cursor *cursorTracker,
	metrics *loaderMetrics,
) (*categoryMap, error) {
	log.Println("=== Stage 2: Categories ===")
	cats := newCategoryMap()

	catDir := filepath.Join(cfg.dataDir, "categories")
	catReader, err := newParquetReader(catDir)
	if err != nil {
		return nil, fmt.Errorf("init category reader: %w", err)
	}
	if err := catReader.ReadCategories(catReader.files[0], cats); err != nil {
		return nil, fmt.Errorf("read categories parquet: %w", err)
	}

	cur := cursor.Get()
	if !cur.CategoriesDone {
		cursor.SetStage("categories")

		catIdx, err := vecdex.NewIndex[Category](client, "fsqr-categories")
		if err != nil {
			return nil, fmt.Errorf("init categories index: %w", err)
		}
		if err := catIdx.Ensure(ctx); err != nil {
			return nil, fmt.Errorf("ensure categories: %w", err)
		}

		loadCategories(ctx, catIdx, cats, metrics)
		cursor.SetCategoriesDone()

		catCount, _ := catIdx.Count(ctx)
		log.Printf("categories done: %d loaded", catCount)
	} else {
		log.Println("categories: skipping (already done)")
	}

	return cats, nil
}

func stageVenues(
	ctx context.Context,
	client *vecdex.Client,
	cfg config,
	cursor *cursorTracker,
	cats *categoryMap,
	metrics *loaderMetrics,
) (ingestResult, error) {
	log.Println("=== Stage 3: Venues ===")
	cursor.SetStage("venues")

	venueIdx, err := vecdex.NewIndex[Venue](client, "fsqr-venues")
	if err != nil {
		return ingestResult{}, fmt.Errorf("init venues index: %w", err)
	}
	if err := venueIdx.Ensure(ctx); err != nil {
		return ingestResult{}, fmt.Errorf("ensure venues: %w", err)
	}

	reader, err := newParquetReader(filepath.Join(cfg.dataDir, "places"))
	if err != nil {
		return ingestResult{}, fmt.Errorf("init parquet reader: %w", err)
	}

	ing := &ingester{
		idx:       venueIdx,
		workers:   cfg.workers,
		batchSize: cfg.batchSize,
		metrics:   metrics,
		cursor:    cursor,
	}

	result, err := ing.Run(ctx, reader, cats, cfg.maxRows)
	if err != nil {
		return result, fmt.Errorf("ingest venues: %w", err)
	}
	return result, nil
}

func stageReport(
	ctx context.Context,
	client *vecdex.Client,
	cats *categoryMap,
	result ingestResult,
	start time.Time,
) {
	log.Println("=== Stage 4: Report ===")

	venueIdx, _ := vecdex.NewIndex[Venue](client, "fsqr-venues")
	venueCount, _ := venueIdx.Count(ctx)
	elapsed := time.Since(start)
	rate := float64(result.Processed) / elapsed.Seconds()

	log.Printf("DONE in %s", elapsed.Round(time.Second))
	log.Printf("  venues: %d in vecdex (%d processed, %d failed)",
		venueCount, result.Processed, result.Failed)
	log.Printf("  rate: %.0f rows/sec", rate)
	log.Printf("  categories: %d", cats.Len())
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
