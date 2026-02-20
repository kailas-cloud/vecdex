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
	dataDir := flag.String("data-dir", "/data", "directory for parquet files and cursor")
	maxRows := flag.Int("max-rows", 1_000_000, "max venues to load (0=unlimited)")
	maxFiles := flag.Int("max-files", 2, "max parquet files to download (0=all)")
	workers := flag.Int("workers", 8, "number of parallel upsert workers")
	batchSize := flag.Int("batch-size", 100, "documents per batch upsert")
	metricsPort := flag.String("metrics-port", "9090", "Prometheus metrics port")
	cursorInterval := flag.Int("cursor-interval", 10000, "save cursor every N rows")
	reset := flag.Bool("reset", false, "reset cursor and start from scratch")
	flag.Parse()

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()

	if err := run(ctx, config{
		dataDir:        *dataDir,
		maxRows:        *maxRows,
		maxFiles:       *maxFiles,
		workers:        *workers,
		batchSize:      *batchSize,
		metricsPort:    *metricsPort,
		cursorInterval: *cursorInterval,
		reset:          *reset,
	}); err != nil {
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

func run(ctx context.Context, cfg config) error {
	start := time.Now()

	// Prometheus.
	reg := prometheus.NewRegistry()
	metrics := newLoaderMetrics(reg)
	prometheus.DefaultRegisterer = reg
	prometheus.DefaultGatherer = reg
	metricsSrv := serveMetrics(cfg.metricsPort)
	defer func() {
		shutCtx, shutCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutCancel()
		_ = metricsSrv.Shutdown(shutCtx)
	}()

	// Cursor.
	cursor, err := newCursorTracker(cfg.dataDir, cfg.cursorInterval)
	if err != nil {
		return fmt.Errorf("cursor: %w", err)
	}
	if cfg.reset {
		cursor.Reset()
		log.Println("cursor reset, starting from scratch")
	}

	// Download parquet files.
	dl := newDownloader(os.Getenv("HF_TOKEN"), cfg.dataDir, metrics)

	placesDir := filepath.Join(cfg.dataDir, "places")
	catDir := filepath.Join(cfg.dataDir, "categories")

	log.Println("=== Stage 1: Download ===")
	placeFiles, err := dl.DownloadPlaces(cfg.maxFiles)
	if err != nil {
		return fmt.Errorf("download places: %w", err)
	}
	catFile, err := dl.DownloadCategories()
	if err != nil {
		return fmt.Errorf("download categories: %w", err)
	}
	_ = placesDir
	_ = catDir

	// Vecdex client.
	addr := env("VALKEY_ADDR", "localhost:6379")
	password := env("VALKEY_PASSWORD", "")
	apiKey := os.Getenv("NEBIUS_API_KEY")

	var opts []vecdex.Option
	opts = append(opts, vecdex.WithValkey(addr, password))
	if apiKey != "" {
		opts = append(opts, vecdex.WithEmbedder(NewNebiusEmbedder(apiKey)))
		opts = append(opts, vecdex.WithVectorDimensions(4096))
	}

	client, err := vecdex.New(ctx, opts...)
	if err != nil {
		return fmt.Errorf("vecdex connect: %w", err)
	}
	defer client.Close()

	// Valkey memory poller (нужен отдельный rueidis client).
	valkeyClient, err := rueidis.NewClient(rueidis.ClientOption{
		InitAddress: []string{addr},
		Password:    password,
	})
	if err != nil {
		log.Printf("warning: cannot connect rueidis for metrics: %v", err)
	} else {
		defer valkeyClient.Close()
		poller := &valkeyPoller{
			client:      valkeyClient,
			metrics:     metrics,
			collections: []string{"fsqr-venues", "fsqr-categories"},
			interval:    30 * time.Second,
			prefix:      "vecdex:",
		}
		poller.Start(ctx)
	}

	// Categories.
	cur := cursor.Get()
	cats := newCategoryMap()

	log.Println("=== Stage 2: Categories ===")
	// Всегда парсим categories parquet для маппинга UUID→int.
	catReader := &parquetReader{files: []string{catFile}}
	if err := catReader.ReadCategories(catFile, cats); err != nil {
		return fmt.Errorf("read categories parquet: %w", err)
	}

	if !cur.CategoriesDone {
		cursor.SetStage("categories")

		catIdx, err := vecdex.NewIndex[Category](client, "fsqr-categories")
		if err != nil {
			return fmt.Errorf("init categories index: %w", err)
		}
		if err := catIdx.Ensure(ctx); err != nil {
			return fmt.Errorf("ensure categories: %w", err)
		}

		if err := loadCategories(ctx, catIdx, cats, metrics); err != nil {
			return fmt.Errorf("load categories: %w", err)
		}
		cursor.SetCategoriesDone()

		catCount, _ := catIdx.Count(ctx)
		log.Printf("categories done: %d loaded", catCount)
	} else {
		log.Println("categories: skipping (already done)")
	}

	// Venues.
	log.Println("=== Stage 3: Venues ===")
	cursor.SetStage("venues")

	venueIdx, err := vecdex.NewIndex[Venue](client, "fsqr-venues")
	if err != nil {
		return fmt.Errorf("init venues index: %w", err)
	}
	if err := venueIdx.Ensure(ctx); err != nil {
		return fmt.Errorf("ensure venues: %w", err)
	}

	// Parquet reader для places.
	reader, err := newParquetReader(filepath.Join(cfg.dataDir, "places"))
	if err != nil {
		// Fallback: используем файлы напрямую если download вернул пути.
		_ = placeFiles
		return fmt.Errorf("init parquet reader: %w", err)
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
		return fmt.Errorf("ingest venues: %w", err)
	}

	// Report.
	log.Println("=== Stage 4: Report ===")
	cursor.Done()

	venueCount, _ := venueIdx.Count(ctx)
	elapsed := time.Since(start)
	rate := float64(result.Processed) / elapsed.Seconds()

	log.Printf("DONE in %s", elapsed.Round(time.Second))
	log.Printf("  venues: %d in vecdex (%d processed, %d failed)", venueCount, result.Processed, result.Failed)
	log.Printf("  rate: %.0f rows/sec", rate)
	log.Printf("  categories: %d", cats.Len())

	return nil
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
