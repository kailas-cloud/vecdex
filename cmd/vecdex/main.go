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
	"github.com/kailas-cloud/vecdex/internal/version"
	"github.com/kailas-cloud/vecdex/internal/db"
	dbRedis "github.com/kailas-cloud/vecdex/internal/db/redis"
	dbValkey "github.com/kailas-cloud/vecdex/internal/db/valkey"
	"github.com/kailas-cloud/vecdex/internal/domain"
	logpkg "github.com/kailas-cloud/vecdex/internal/logger"
	"github.com/kailas-cloud/vecdex/internal/metrics"
	budgetrepo "github.com/kailas-cloud/vecdex/internal/repository/budget"
	collectionrepo "github.com/kailas-cloud/vecdex/internal/repository/collection"
	documentrepo "github.com/kailas-cloud/vecdex/internal/repository/document"
	"github.com/kailas-cloud/vecdex/internal/repository/embcache"
	searchrepo "github.com/kailas-cloud/vecdex/internal/repository/search"
	chiTransport "github.com/kailas-cloud/vecdex/internal/transport/chi"
	gen "github.com/kailas-cloud/vecdex/internal/transport/generated"
	openaiEmb "github.com/kailas-cloud/vecdex/internal/transport/openai"
	batchuc "github.com/kailas-cloud/vecdex/internal/usecase/batch"
	collectionuc "github.com/kailas-cloud/vecdex/internal/usecase/collection"
	documentuc "github.com/kailas-cloud/vecdex/internal/usecase/document"
	embeddinguc "github.com/kailas-cloud/vecdex/internal/usecase/embedding"
	healthuc "github.com/kailas-cloud/vecdex/internal/usecase/health"
	searchuc "github.com/kailas-cloud/vecdex/internal/usecase/search"
	usageuc "github.com/kailas-cloud/vecdex/internal/usecase/usage"
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
		zap.String("db_driver", cfg.Database.Driver),
		zap.Strings("db_addrs", cfg.Database.Addrs),
	)

	// Create database store based on driver
	var store db.Store
	switch cfg.Database.Driver {
	case "valkey":
		store, err = dbValkey.NewStore(dbValkey.Config{
			Addrs:    cfg.Database.Addrs,
			Password: cfg.Database.Password,
		})
	case "redis":
		store, err = dbRedis.NewStore(dbRedis.Config{
			Addrs:    cfg.Database.Addrs,
			Password: cfg.Database.Password,
		})
	default:
		logger.Fatal("Unknown database driver", zap.String("driver", cfg.Database.Driver))
	}
	if err != nil {
		logger.Fatal("Failed to create database store", zap.Error(err))
	}
	defer store.Close()

	// Wait for database to be ready
	ctx := context.Background()
	if err := store.WaitForReady(ctx, time.Duration(cfg.Database.ReadinessTimeout)*time.Second); err != nil {
		logger.Fatal("Database not ready", zap.Error(err))
	}
	logger.Info("Connected to database")

	// Register embedding metrics explicitly (no init())
	metrics.RegisterEmbeddingMetrics()

	// Build embedder chain — composition root
	// Take the first vectorizer config
	var vecCfg config.VectorizerConfig
	var provName string
	for _, vc := range cfg.Embedding.Vectorizers {
		vecCfg = vc
		provName = vc.Provider
		break
	}
	provCfg := cfg.Embedding.Providers[provName]

	// Single BudgetTracker shared across all embedders and usage service.
	var budget *embeddinguc.BudgetTracker
	budgetCfg := provCfg.Budget
	if budgetCfg.DailyTokenLimit > 0 || budgetCfg.MonthlyTokenLimit > 0 {
		action := embeddinguc.BudgetActionWarn
		if budgetCfg.Action == "reject" {
			action = embeddinguc.BudgetActionReject
		}
		budget = embeddinguc.NewBudgetTracker(
			provName, budgetCfg.DailyTokenLimit, budgetCfg.MonthlyTokenLimit, action, logger,
		)
		// Connect persistence store — loads current counters from DB.
		budgetStore := budgetrepo.New(store, 48*time.Hour, 62*24*time.Hour)
		budget.WithStore(ctx, budgetStore)
	}

	// Pass nil interface (not typed nil pointer!) if budget is not configured.
	// Go gotcha: (*BudgetTracker)(nil) wrapped in BudgetChecker != nil.
	var budgetChecker embeddinguc.BudgetChecker
	if budget != nil {
		budgetChecker = budget
	}

	docEmbedder := buildEmbedder(
		provName, provCfg, vecCfg, vecCfg.DocumentInstruction,
		store, budgetChecker, logger,
	)
	queryEmbedder := buildEmbedder(
		provName, provCfg, vecCfg, vecCfg.QueryInstruction,
		store, budgetChecker, logger,
	)
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

	// Create use case services
	collSvc := collectionuc.New(collRepo, vectorDim)
	docSvc := documentuc.New(docRepo, collRepo, docEmbedder, queryEmbedder).
		WithPagination(cfg.Index.DefaultPageSize, cfg.Index.MaxPageSize)
	searchSvc := searchuc.New(searchRepo, collRepo, queryEmbedder)
	batchSvc := batchuc.New(docRepo, docRepo, collRepo, docEmbedder).
		WithMaxBatchSize(cfg.Index.MaxBatchSize)

	// Usage service — reads from shared BudgetTracker
	var budgetReader usageuc.BudgetReader
	if budget != nil {
		budgetReader = budget
	}
	usageSvc := usageuc.New(budgetReader)

	// Health service
	healthSvc := healthuc.New(store, newEmbeddingHealthChecker(docEmbedder))

	// Create chi server
	server := chiTransport.NewServer(collSvc, docSvc, searchSvc, batchSvc, usageSvc, healthSvc, logger)

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
	provCfg config.ProviderConfig,
	vecCfg config.VectorizerConfig,
	instruction string,
	store db.Store,
	budget embeddinguc.BudgetChecker,
	logger *zap.Logger,
) domain.Embedder {
	// Base provider (with transport metrics built-in)
	base := openaiEmb.NewEmbedder(&openaiEmb.Config{
		APIKey:     provCfg.APIKey,
		BaseURL:    provCfg.BaseURL,
		Model:      vecCfg.Model,
		Dimensions: vecCfg.Dimensions,
		Provider:   provName,
		Logger:     logger,
	})

	// Cached
	var embedder domain.Embedder = base
	if store != nil {
		embedder = embcache.New(base, store, metrics.EmbeddingCacheTotal, logger)
	}

	// Instrumented (budget + metrics)
	embedder = embeddinguc.NewInstrumentedEmbedder(
		embedder, provName, vecCfg.Model, budget, logger,
	)

	// Instruction prefix (outermost — cache key includes instruction)
	if instruction != "" {
		return domain.NewInstructionEmbedder(embedder, instruction)
	}

	return embedder
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
