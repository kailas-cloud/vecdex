// Package app composes the production engine independently of its transport.
package app

import (
	"context"
	"fmt"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/kailas-cloud/vecdex/internal/config"
	"github.com/kailas-cloud/vecdex/internal/db"
	dbValkey "github.com/kailas-cloud/vecdex/internal/db/valkey"
	"github.com/kailas-cloud/vecdex/internal/domain"
	domusage "github.com/kailas-cloud/vecdex/internal/domain/usage"
	"github.com/kailas-cloud/vecdex/internal/memorycontext"
	"github.com/kailas-cloud/vecdex/internal/metrics"
	budgetrepo "github.com/kailas-cloud/vecdex/internal/repository/budget"
	collectionrepo "github.com/kailas-cloud/vecdex/internal/repository/collection"
	documentrepo "github.com/kailas-cloud/vecdex/internal/repository/document"
	"github.com/kailas-cloud/vecdex/internal/repository/embcache"
	searchrepo "github.com/kailas-cloud/vecdex/internal/repository/search"
	bedrockEmb "github.com/kailas-cloud/vecdex/internal/transport/bedrock"
	onnxEmb "github.com/kailas-cloud/vecdex/internal/transport/onnx"
	openaiEmb "github.com/kailas-cloud/vecdex/internal/transport/openai"
	batchuc "github.com/kailas-cloud/vecdex/internal/usecase/batch"
	collectionuc "github.com/kailas-cloud/vecdex/internal/usecase/collection"
	documentuc "github.com/kailas-cloud/vecdex/internal/usecase/document"
	embeddinguc "github.com/kailas-cloud/vecdex/internal/usecase/embedding"
	healthuc "github.com/kailas-cloud/vecdex/internal/usecase/health"
	searchuc "github.com/kailas-cloud/vecdex/internal/usecase/search"
	usageuc "github.com/kailas-cloud/vecdex/internal/usecase/usage"
)

// Runtime owns services and background maintenance for a server instance.
type Runtime struct {
	Store       *dbValkey.Store
	Collections *collectionuc.Service
	Documents   *documentuc.Service
	Search      *searchuc.Service
	Batch       *batchuc.Service
	Usage       *usageuc.Service
	Health      *healthuc.Service
	Contexts    *memorycontext.Service
	stops       []func()
	once        sync.Once
}

// New builds the production embedding, accounting and storage recovery chain.
func New(ctx context.Context, cfg *config.Config, logger *zap.Logger) (*Runtime, error) {
	store, err := dbValkey.NewStore(dbValkey.Config{
		Addrs:    cfg.Valkey.Addrs,
		Password: cfg.Valkey.Password,
	})
	if err != nil {
		return nil, fmt.Errorf("create Valkey store: %w", err)
	}
	rt := &Runtime{Store: store}
	complete := false
	defer func() {
		if !complete {
			rt.Close()
		}
	}()

	if err := store.WaitForReady(ctx, time.Duration(cfg.Valkey.ReadinessTimeout)*time.Second); err != nil {
		return nil, fmt.Errorf("valkey readiness: %w", err)
	}
	logger.Info("Connected to Valkey")

	// Register embedding metrics explicitly (no init())
	metrics.RegisterEmbeddingMetrics()

	docEmbedder, queryEmbedder, budget, vecCfg, err := rt.embedders(ctx, cfg, logger)
	if err != nil {
		return nil, err
	}

	rt.services(ctx, cfg, logger, docEmbedder, queryEmbedder, budget, vecCfg)
	complete = true
	return rt, nil
}

// Close stops maintenance before closing its database connection.
func (r *Runtime) Close() {
	r.once.Do(func() {
		for i := len(r.stops) - 1; i >= 0; i-- {
			r.stops[i]()
		}
		if r.Store != nil {
			r.Store.Close()
		}
	})
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

func invocationObserver(budget embeddinguc.BudgetChecker) domusage.InvocationObserver {
	observer, _ := budget.(domusage.InvocationObserver)
	return observer
}

func (rt *Runtime) embedders(ctx context.Context, cfg *config.Config, logger *zap.Logger) (
	document domain.Embedder, query domain.Embedder, tracker *embeddinguc.BudgetTracker,
	vectorizer config.VectorizerConfig, resultErr error,
) {
	// Build embedder chain — composition root
	_, vecCfg, err := cfg.ActiveVectorizer()
	if err != nil {
		return nil, nil, nil, config.VectorizerConfig{}, fmt.Errorf("active vectorizer: %w", err)
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
		WithAccountingStore(budgetrepo.NewAccounting(rt.Store), maxInputTokens)
	stopBudgetMaintenance := budget.StartMaintenance(ctx)
	rt.stops = append(rt.stops, stopBudgetMaintenance)
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
		rt.Store, budgetChecker, providerSlots, logger,
	)
	if err != nil {
		return nil, nil, nil, config.VectorizerConfig{}, fmt.Errorf("document embedder: %w", err)
	}
	queryEmbedder, err := buildEmbedder(
		provName, &provCfg, vecCfg, "search_query", vecCfg.QueryInstruction,
		rt.Store, budgetChecker, providerSlots, logger,
	)
	if err != nil {
		return nil, nil, nil, config.VectorizerConfig{}, fmt.Errorf("query embedder: %w", err)
	}
	logger.Info("Embedders created",
		zap.String("provider", provName),
		zap.String("model", vecCfg.Model),
		zap.Int("dimensions", vecCfg.Dimensions),
	)

	return docEmbedder, queryEmbedder, budget, vecCfg, nil
}

func (rt *Runtime) services(ctx context.Context, cfg *config.Config, logger *zap.Logger,
	docEmbedder, queryEmbedder domain.Embedder, budget *embeddinguc.BudgetTracker, vecCfg config.VectorizerConfig,
) {
	// Create repositories (domain-native, no adapters)
	vectorDim := vecCfg.Dimensions
	if vectorDim == 0 {
		vectorDim = domain.DefaultVectorConfig().Dimensions
	}

	collRepo := collectionrepo.New(rt.Store, vectorDim).WithHNSW(collectionrepo.HNSWConfig{
		M:           cfg.Index.HNSWM,
		EFConstruct: cfg.Index.HNSWEFConstruct,
	})
	stopMaintenance := collRepo.StartMaintenance(ctx, func(err error) {
		logger.Warn("Storage recovery pass failed", zap.Error(err))
	})
	rt.stops = append(rt.stops, stopMaintenance)
	docRepo := documentrepo.New(rt.Store)
	searchRepo := searchrepo.New(rt.Store)

	provider := cfg.Embedding.Providers[vecCfg.Provider]
	embeddingSpace := vecCfg.Fingerprint(&provider)
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
	healthSvc := healthuc.New(rt.Store, newEmbeddingHealthChecker(docEmbedder))

	rt.Collections, rt.Documents, rt.Search, rt.Batch = collSvc, docSvc, searchSvc, batchSvc
	rt.Usage, rt.Health = usageSvc, healthSvc
	rt.Contexts = memorycontext.New(rt.Store.Client())
}
