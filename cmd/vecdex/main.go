package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	chiMiddleware "github.com/go-chi/chi/v5/middleware"
	"go.uber.org/zap"

	"github.com/kailas-cloud/vecdex/internal/config"
	"github.com/kailas-cloud/vecdex/internal/db"
	dbValkey "github.com/kailas-cloud/vecdex/internal/db/valkey"
	"github.com/kailas-cloud/vecdex/internal/domain"
	domusage "github.com/kailas-cloud/vecdex/internal/domain/usage"
	logpkg "github.com/kailas-cloud/vecdex/internal/logger"
	"github.com/kailas-cloud/vecdex/internal/metrics"
	budgetrepo "github.com/kailas-cloud/vecdex/internal/repository/budget"
	collectionrepo "github.com/kailas-cloud/vecdex/internal/repository/collection"
	documentrepo "github.com/kailas-cloud/vecdex/internal/repository/document"
	"github.com/kailas-cloud/vecdex/internal/repository/embcache"
	searchrepo "github.com/kailas-cloud/vecdex/internal/repository/search"
	bedrockEmb "github.com/kailas-cloud/vecdex/internal/transport/bedrock"
	chiTransport "github.com/kailas-cloud/vecdex/internal/transport/chi"
	gen "github.com/kailas-cloud/vecdex/internal/transport/generated"
	onnxEmb "github.com/kailas-cloud/vecdex/internal/transport/onnx"
	openaiEmb "github.com/kailas-cloud/vecdex/internal/transport/openai"
	batchuc "github.com/kailas-cloud/vecdex/internal/usecase/batch"
	collectionuc "github.com/kailas-cloud/vecdex/internal/usecase/collection"
	documentuc "github.com/kailas-cloud/vecdex/internal/usecase/document"
	embeddinguc "github.com/kailas-cloud/vecdex/internal/usecase/embedding"
	healthuc "github.com/kailas-cloud/vecdex/internal/usecase/health"
	searchuc "github.com/kailas-cloud/vecdex/internal/usecase/search"
	usageuc "github.com/kailas-cloud/vecdex/internal/usecase/usage"
	"github.com/kailas-cloud/vecdex/internal/version"
)

func main() {
	// Load configuration based on ENV
	env := config.GetEnv()

	cfg, err := config.Load(env)
	if err != nil {
		panic("failed to load config: " + err.Error())
	}

	logger, err := logpkg.NewLogger(env, cfg.Logging.Level)
	if err != nil {
		panic("failed to create logger: " + err.Error())
	}
	defer func() { _ = logger.Sync() }()

	logger.Info("Starting vecdex API server",
		zap.String("version", version.Version),
		zap.String("commit", version.Commit),
		zap.String("env", env),
		zap.Int("http_port", cfg.HTTP.Port),
		zap.Strings("valkey_addrs", cfg.Valkey.Addrs),
	)

	var store db.Store
	store, err = dbValkey.NewStore(dbValkey.Config{
		Addrs:    cfg.Valkey.Addrs,
		Password: cfg.Valkey.Password,
	})
	if err != nil {
		logger.Fatal("Failed to create Valkey store", zap.Error(err))
	}
	defer store.Close()

	ctx := context.Background()
	if err := store.WaitForReady(ctx, time.Duration(cfg.Valkey.ReadinessTimeout)*time.Second); err != nil {
		logger.Fatal("Valkey not ready", zap.Error(err))
	}
	logger.Info("Connected to Valkey")

	// Register embedding metrics explicitly (no init())
	metrics.RegisterEmbeddingMetrics()

	// Build embedder chain — composition root
	_, vecCfg, err := cfg.ActiveVectorizer()
	if err != nil {
		logger.Fatal("Invalid active vectorizer", zap.Error(err))
	}
	provName := vecCfg.Provider

	provCfg := cfg.Embedding.Providers[provName]

	// Accounting is always active, including when no limits are configured.
	budgetCfg := provCfg.Budget
	action := embeddinguc.BudgetActionWarn
	if budgetCfg.Action == "reject" {
		action = embeddinguc.BudgetActionReject
	}
	maxInputTokens := int64(128000)
	if provCfg.Backend == "onnx" {
		maxInputTokens = int64(provCfg.MaxLength)
	}
	budget := embeddinguc.NewBudgetTracker(
		provName, budgetCfg.DailyTokenLimit, budgetCfg.MonthlyTokenLimit, action, logger,
	).
		WithAccountingStore(budgetrepo.NewAccounting(store), maxInputTokens)
	var budgetChecker embeddinguc.BudgetChecker = budget
	var providerSlots chan struct{}
	if provCfg.Backend == "bedrock" {
		concurrency := provCfg.MaxConcurrency
		if concurrency == 0 {
			concurrency = 4
		}
		providerSlots = make(chan struct{}, concurrency)
	}

	docEmbedder, err := buildEmbedder(
		provName, &provCfg, vecCfg, "search_document", vecCfg.DocumentInstruction,
		store, budgetChecker, providerSlots, logger,
	)
	if err != nil {
		logger.Fatal("Failed to create document embedder", zap.Error(err))
	}
	queryEmbedder, err := buildEmbedder(
		provName, &provCfg, vecCfg, "search_query", vecCfg.QueryInstruction,
		store, budgetChecker, providerSlots, logger,
	)
	if err != nil {
		logger.Fatal("Failed to create query embedder", zap.Error(err))
	}
	logger.Info("Embedders created",
		zap.String("provider", provName),
		zap.String("model", vecCfg.Model),
		zap.Int("dimensions", vecCfg.Dimensions),
	)

	// Create repositories (domain-native, no adapters)
	vectorDim := vecCfg.Dimensions
	if vectorDim == 0 {
		vectorDim = domain.DefaultVectorConfig().Dimensions
	}

	collRepo := collectionrepo.New(store, vectorDim).WithHNSW(collectionrepo.HNSWConfig{
		M:           cfg.Index.HNSWM,
		EFConstruct: cfg.Index.HNSWEFConstruct,
	})
	docRepo := documentrepo.New(store)
	searchRepo := searchrepo.New(store)

	embeddingSpace := vecCfg.Fingerprint(&provCfg)
	// Create use case services
	collSvc := collectionuc.New(collRepo, vectorDim).WithEmbeddingSpace(embeddingSpace)
	docSvc := documentuc.New(docRepo, collRepo, docEmbedder, queryEmbedder).
		WithPagination(cfg.Index.DefaultPageSize, cfg.Index.MaxPageSize).WithEmbeddingSpace(embeddingSpace)
	searchSvc := searchuc.New(searchRepo, collRepo, queryEmbedder).
		WithDocuments(docRepo).
		WithEmbeddingSpace(embeddingSpace)
	searchSvc = searchSvc.WithConfig(searchuc.Config{
		SemanticCandidateFloor:      cfg.Search.SemanticCandidateFloor,
		SemanticCandidateMultiplier: cfg.Search.SemanticCandidateMultiplier,
		BM25CandidateFloor:          cfg.Search.BM25CandidateFloor,
		BM25CandidateMultiplier:     cfg.Search.BM25CandidateMultiplier,
	})
	// Type-assert: если docEmbedder поддерживает batch — передаём в batch service
	var batchEmb batchuc.BulkEmbedder
	if be, ok := docEmbedder.(domain.BatchEmbedder); ok {
		batchEmb = be
	}
	batchSvc := batchuc.New(docRepo, docRepo, docRepo, collRepo, docEmbedder, batchEmb).
		WithMaxBatchSize(cfg.Index.MaxBatchSize).WithEmbeddingSpace(embeddingSpace)

	// Usage service — reads from shared BudgetTracker
	var budgetReader usageuc.BudgetReader
	if budget != nil {
		budgetReader = budget
	}
	usageSvc := usageuc.New(budgetReader)

	// Health service
	healthSvc := healthuc.New(store, newEmbeddingHealthChecker(docEmbedder))

	// Create chi server
	server := chiTransport.NewServer(collSvc, docSvc, searchSvc, batchSvc, usageSvc, healthSvc, logger).
		WithMaxBatchSize(cfg.Index.MaxBatchSize)

	r := chi.NewRouter()
	r.Use(jsonRecoverer(logger))
	r.Use(chiMiddleware.RequestID)
	r.Use(wideEventMiddleware(logger))
	r.Use(chiTransport.BearerAuthMiddleware(cfg.Auth.APIKeys))
	r.Use(metrics.Middleware())
	gen.HandlerWithOptions(server, gen.ChiServerOptions{
		BaseRouter: r,
		ErrorHandlerFunc: func(w http.ResponseWriter, _ *http.Request, err error) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(gen.ErrorResponse{
				Code:    gen.ErrorResponseCodeBadRequest,
				Message: "invalid request",
			})
		},
	})

	addr := fmt.Sprintf(":%d", cfg.HTTP.Port)
	srv := &http.Server{
		Addr:         addr,
		Handler:      r,
		ReadTimeout:  time.Duration(cfg.HTTP.ReadTimeoutSec) * time.Second,
		WriteTimeout: time.Duration(cfg.HTTP.WriteTimeoutSec) * time.Second,
	}

	// Graceful shutdown
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, os.Interrupt, syscall.SIGTERM)

	go func() {
		logger.Info("Starting HTTP server", zap.String("addr", addr))
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Fatal("HTTP server error", zap.Error(err))
		}
	}()

	<-quit
	logger.Info("Received shutdown signal")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Duration(cfg.HTTP.ShutdownSec)*time.Second)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Error("Error during shutdown", zap.Error(err))
	}

	logger.Info("Server stopped gracefully")
}

// embeddingHealthChecker wraps domain.Embedder to implement health.EmbeddingChecker.
type embeddingHealthChecker struct {
	embedder domain.Embedder
}

func newEmbeddingHealthChecker(embedder domain.Embedder) *embeddingHealthChecker {
	return &embeddingHealthChecker{embedder: embedder}
}

func (h *embeddingHealthChecker) HealthCheck(ctx context.Context) error {
	if hc, ok := h.embedder.(domain.HealthChecker); ok {
		if err := hc.HealthCheck(ctx); err != nil {
			return fmt.Errorf("embedding health check: %w", err)
		}
	}
	return nil
}

// buildEmbedder assembles the decorator chain: OpenAI -> Cached -> Instrumented -> Instruction
func buildEmbedder(
	provName string,
	provCfg *config.ProviderConfig,
	vecCfg config.VectorizerConfig,
	purpose string,
	instruction string,
	store db.Store,
	budget embeddinguc.BudgetChecker,
	providerSlots chan struct{},
	logger *zap.Logger,
) (domain.Embedder, error) {
	var (
		base domain.Embedder
		err  error
	)

	switch provCfg.Backend {
	case "", "openai":
		base = openaiEmb.NewEmbedder(&openaiEmb.Config{
			APIKey:     provCfg.APIKey,
			BaseURL:    provCfg.BaseURL,
			Model:      vecCfg.Model,
			Dimensions: vecCfg.Dimensions,
			Provider:   provName,
			Logger:     logger,
			Observer:   invocationObserver(budget),
		})
	case "bedrock":
		base, err = bedrockEmb.NewEmbedder(&bedrockEmb.Config{
			APIKey: provCfg.APIKey, Region: provCfg.Region, Model: vecCfg.Model, Provider: provName,
			Dimensions: vecCfg.Dimensions, Purpose: purpose,
			Timeout: time.Duration(provCfg.TimeoutSec) * time.Second, MaxConcurrency: provCfg.MaxConcurrency,
			Observer:           invocationObserver(budget),
			ConcurrencyLimiter: providerSlots,
		})
		if err != nil {
			return nil, fmt.Errorf("create Bedrock embedder: %w", err)
		}
	case "onnx":
		base, err = onnxEmb.NewEmbedder(&onnxEmb.Config{
			ModelDir:          provCfg.ModelDir,
			Model:             vecCfg.Model,
			Dimensions:        vecCfg.Dimensions,
			MaxLength:         provCfg.MaxLength,
			ExecutionProvider: provCfg.ExecutionProvider,
			Observer:          invocationObserver(budget),
			Provider:          provName,
			Logger:            logger,
		})
		if err != nil {
			return nil, fmt.Errorf("create onnx embedder: %w", err)
		}
		base = embeddinguc.NewParallelBatchEmbedder(base, 10, 10)
	default:
		return nil, fmt.Errorf("unsupported embedding backend %q", provCfg.Backend)
	}

	// Cached
	embedder := base
	if store != nil {
		embedder = embcache.New(base, store, metrics.EmbeddingCacheTotal, logger).
			WithNamespace(vecCfg.Fingerprint(provCfg) + "|" + purpose).
			WithTTL(time.Duration(provCfg.CacheTTLSec) * time.Second).
			WithDimensions(vecCfg.Dimensions)
	}

	// Instrumented (budget + metrics)
	embedder = embeddinguc.NewInstrumentedEmbedder(
		embedder, provName, vecCfg.Model, nil, logger,
	)

	// Instruction prefix (outermost — cache key includes instruction)
	if instruction != "" {
		return domain.NewInstructionEmbedder(embedder, instruction), nil
	}

	return embedder, nil
}

// jsonRecoverer is a recovery middleware that returns JSON instead of a plain text stacktrace.
func jsonRecoverer(logger *zap.Logger) func(next http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if rvr := recover(); rvr != nil {
					logger.Error("panic recovered",
						zap.Any("panic", rvr),
						zap.Stack("stacktrace"),
					)
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusInternalServerError)
					_ = json.NewEncoder(w).Encode(map[string]string{
						"code":    "internal_error",
						"message": "internal error",
					})
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// wideEventMiddleware emits a canonical log line per request and propagates X-Request-ID.
func wideEventMiddleware(logger *zap.Logger) func(next http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()

			// chi.middleware.RequestID already placed request_id in context
			requestID := chiMiddleware.GetReqID(r.Context())

			// Set X-Request-ID in response header
			if requestID != "" {
				w.Header().Set("X-Request-ID", requestID)
			}

			// Per-request logger with request_id
			reqLogger := logger.With(zap.String("request_id", requestID))
			ctx := logpkg.ContextWithLogger(r.Context(), reqLogger)

			ww := chiMiddleware.NewWrapResponseWriter(w, r.ProtoMajor)
			next.ServeHTTP(ww, r.WithContext(ctx))

			// Canonical log line — one line per request
			reqLogger.Info("http_request",
				zap.String("method", r.Method),
				zap.String("path", r.URL.Path),
				zap.Int("status", ww.Status()),
				zap.Duration("latency", time.Since(start)),
				zap.String("ip", r.RemoteAddr),
				zap.Int64("content_length", r.ContentLength),
				zap.String("user_agent", r.UserAgent()),
				zap.Int("response_bytes", ww.BytesWritten()),
			)
		})
	}
}

func invocationObserver(budget embeddinguc.BudgetChecker) domusage.InvocationObserver {
	observer, _ := budget.(domusage.InvocationObserver)
	return observer
}
