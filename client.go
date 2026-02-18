package vecdex

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/kailas-cloud/vecdex/internal/db"
	dbRedis "github.com/kailas-cloud/vecdex/internal/db/redis"
	dbValkey "github.com/kailas-cloud/vecdex/internal/db/valkey"
	"github.com/kailas-cloud/vecdex/internal/domain"
	collectionrepo "github.com/kailas-cloud/vecdex/internal/repository/collection"
	documentrepo "github.com/kailas-cloud/vecdex/internal/repository/document"
	searchrepo "github.com/kailas-cloud/vecdex/internal/repository/search"
	batchuc "github.com/kailas-cloud/vecdex/internal/usecase/batch"
	collectionuc "github.com/kailas-cloud/vecdex/internal/usecase/collection"
	documentuc "github.com/kailas-cloud/vecdex/internal/usecase/document"
	searchuc "github.com/kailas-cloud/vecdex/internal/usecase/search"
)

const defaultReadinessTimeout = 10 * time.Second

// Client is the vecdex SDK entry point.
type Client struct {
	store     db.Store
	collSvc   *collectionuc.Service
	docSvc    *documentuc.Service
	searchSvc *searchuc.Service
	batchSvc  *batchuc.Service
}

// New creates a vecdex Client and connects to the database.
func New(opts ...Option) (*Client, error) {
	cfg := &clientConfig{
		vectorDimensions: domain.DefaultVectorConfig().Dimensions,
	}
	for _, o := range opts {
		o(cfg)
	}

	if len(cfg.addrs) == 0 {
		return nil, errors.New("vecdex: database address required (use WithValkey or WithRedis)")
	}

	store, err := createStore(cfg)
	if err != nil {
		return nil, err
	}

	ctx := context.Background()
	if err := store.WaitForReady(ctx, defaultReadinessTimeout); err != nil {
		store.Close()
		return nil, fmt.Errorf("vecdex: database not ready: %w", err)
	}

	return wireClient(store, cfg)
}

func createStore(cfg *clientConfig) (db.Store, error) {
	switch cfg.driver {
	case "valkey":
		s, err := dbValkey.NewStore(dbValkey.Config{
			Addrs:    cfg.addrs,
			Password: cfg.password,
		})
		if err != nil {
			return nil, fmt.Errorf("vecdex: create valkey store: %w", err)
		}
		return s, nil
	case "redis":
		s, err := dbRedis.NewStore(dbRedis.Config{
			Addrs:    cfg.addrs,
			Password: cfg.password,
		})
		if err != nil {
			return nil, fmt.Errorf("vecdex: create redis store: %w", err)
		}
		return s, nil
	default:
		return nil, fmt.Errorf("vecdex: unknown driver %q", cfg.driver)
	}
}

func wireClient(store db.Store, cfg *clientConfig) (*Client, error) {
	vectorDim := cfg.vectorDimensions

	collRepo := collectionrepo.New(store, vectorDim)
	if cfg.hnswM > 0 || cfg.hnswEFConstruct > 0 {
		collRepo = collRepo.WithHNSW(collectionrepo.HNSWConfig{
			M:           cfg.hnswM,
			EFConstruct: cfg.hnswEFConstruct,
		})
	}
	docRepo := documentrepo.New(store)
	searchRepo := searchrepo.New(store)

	// Embedder: noop если не задан (geo работает, text вернёт ошибку)
	var domEmb domain.Embedder = &noopEmbedder{}
	if cfg.embedder != nil {
		domEmb = &embedderAdapter{inner: cfg.embedder}
	}

	collSvc := collectionuc.New(collRepo, vectorDim)
	docSvc := documentuc.New(docRepo, collRepo, domEmb, domEmb)
	searchSvc := searchuc.New(searchRepo, collRepo, domEmb)
	batchSvc := batchuc.New(docRepo, docRepo, docRepo, collRepo, domEmb)
	if cfg.maxBatchSize > 0 {
		batchSvc = batchSvc.WithMaxBatchSize(cfg.maxBatchSize)
	}

	return &Client{
		store:     store,
		collSvc:   collSvc,
		docSvc:    docSvc,
		searchSvc: searchSvc,
		batchSvc:  batchSvc,
	}, nil
}

// Close releases all resources.
func (c *Client) Close() {
	if c.store != nil {
		c.store.Close()
	}
}

// Ping checks database connectivity.
func (c *Client) Ping(ctx context.Context) error {
	if err := c.store.Ping(ctx); err != nil {
		return fmt.Errorf("ping: %w", err)
	}
	return nil
}

// Collections returns the collection management service.
func (c *Client) Collections() *CollectionService {
	return &CollectionService{svc: c.collSvc}
}

// Documents returns the document service for a given collection.
func (c *Client) Documents(collection string) *DocumentService {
	return &DocumentService{
		collection: collection,
		docSvc:     c.docSvc,
		batchSvc:   c.batchSvc,
	}
}

// Search returns the search service for a given collection.
func (c *Client) Search(collection string) *SearchService {
	return &SearchService{collection: collection, svc: c.searchSvc}
}

// embedderAdapter wraps public Embedder to satisfy internal domain.Embedder.
type embedderAdapter struct {
	inner Embedder
}

func (a *embedderAdapter) Embed(ctx context.Context, text string) (domain.EmbeddingResult, error) {
	r, err := a.inner.Embed(ctx, text)
	if err != nil {
		return domain.EmbeddingResult{}, fmt.Errorf("embed: %w", err)
	}
	return domain.EmbeddingResult{
		Embedding:    r.Embedding,
		PromptTokens: r.PromptTokens,
		TotalTokens:  r.TotalTokens,
	}, nil
}

// noopEmbedder returns an error on Embed call (used when no embedder configured).
type noopEmbedder struct{}

func (noopEmbedder) Embed(_ context.Context, _ string) (domain.EmbeddingResult, error) {
	return domain.EmbeddingResult{}, errors.New(
		"vecdex: embedder not configured (use WithEmbedder for text collections)",
	)
}
