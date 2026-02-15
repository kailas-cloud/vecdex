package collection

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/kailas-cloud/vecdex/internal/db"
	"github.com/kailas-cloud/vecdex/internal/domain"
	domcol "github.com/kailas-cloud/vecdex/internal/domain/collection"
)

// store is the consumer interface for collections (ISP).
//
//nolint:interfacebloat // collection repo needs hash + index management operations
type store interface {
	HSet(ctx context.Context, key string, fields map[string]string) error
	HGetAll(ctx context.Context, key string) (map[string]string, error)
	HGetAllMulti(ctx context.Context, keys []string) ([]map[string]string, error)
	Del(ctx context.Context, key string) error
	Exists(ctx context.Context, key string) (bool, error)
	Scan(ctx context.Context, pattern string) ([]string, error)
	CreateIndex(ctx context.Context, def *db.IndexDefinition) error
	DropIndex(ctx context.Context, name string) error
	IndexExists(ctx context.Context, name string) (bool, error)
	SupportsTextSearch(ctx context.Context) bool
}

// HNSWConfig HNSW index parameters.
type HNSWConfig struct {
	M           int
	EFConstruct int
}

// Repo implements usecase/collection.Repository.
type Repo struct {
	store            store
	defaultVectorDim int
	hnsw             HNSWConfig
}

// New creates a collection repository.
func New(s store, defaultVectorDim int) *Repo {
	return &Repo{store: s, defaultVectorDim: defaultVectorDim, hnsw: HNSWConfig{M: 32, EFConstruct: 400}}
}

// WithHNSW configures HNSW index parameters.
func (r *Repo) WithHNSW(cfg HNSWConfig) *Repo {
	if cfg.M > 0 {
		r.hnsw.M = cfg.M
	}
	if cfg.EFConstruct > 0 {
		r.hnsw.EFConstruct = cfg.EFConstruct
	}
	return r
}

// Create stores a collection: HSET metadata then FT.CREATE index.
// On FT.CREATE failure, rolls back the HSET via DEL.
func (r *Repo) Create(ctx context.Context, col domcol.Collection) error {
	name := col.Name()

	metaKey := metaKey(name)
	exists, err := r.store.Exists(ctx, metaKey)
	if err != nil {
		return fmt.Errorf("check exists: %w", err)
	}
	if exists {
		return domain.ErrAlreadyExists
	}

	// Prepare index definition and hash data before writes
	indexDef, err := buildIndex(name, col.Fields(), col.VectorDim(), r.store.SupportsTextSearch(ctx), r.hnsw)
	if err != nil {
		return fmt.Errorf("build index: %w", err)
	}
	hashData, err := collectionToHash(col)
	if err != nil {
		return err
	}

	// Step 1: HSET metadata
	if err := r.store.HSet(ctx, metaKey, hashData); err != nil {
		return fmt.Errorf("hset collection %s: %w", name, err)
	}

	// FT.CREATE — rollback HSET on error
	if err := r.store.CreateIndex(ctx, indexDef); err != nil {
		cleanupErr := r.store.Del(ctx, metaKey)
		return errors.Join(err, cleanupErr)
	}

	return nil
}

// Get retrieves a collection by name.
func (r *Repo) Get(ctx context.Context, name string) (domcol.Collection, error) {
	m, err := r.store.HGetAll(ctx, metaKey(name))
	if err != nil {
		return domcol.Collection{}, fmt.Errorf("hgetall collection %s: %w", name, err)
	}
	if len(m) == 0 {
		return domcol.Collection{}, domain.ErrNotFound
	}

	return collectionFromHash(m, r.defaultVectorDim)
}

// List returns all collections sorted by CreatedAt.
func (r *Repo) List(ctx context.Context) ([]domcol.Collection, error) {
	keys, err := r.store.Scan(ctx, metaKey("*"))
	if err != nil {
		return nil, fmt.Errorf("scan collections: %w", err)
	}
	if len(keys) == 0 {
		return []domcol.Collection{}, nil
	}

	results, err := r.store.HGetAllMulti(ctx, keys)
	if err != nil {
		return nil, fmt.Errorf("hgetall multi collections: %w", err)
	}

	collections := make([]domcol.Collection, 0, len(results))
	for i, m := range results {
		if len(m) == 0 {
			continue
		}
		col, err := collectionFromHash(m, r.defaultVectorDim)
		if err != nil {
			return nil, fmt.Errorf("parse collection %s: %w", keys[i], err)
		}
		collections = append(collections, col)
	}

	sort.Slice(collections, func(i, j int) bool {
		return collections[i].CreatedAt() < collections[j].CreatedAt()
	})

	return collections, nil
}

// Delete removes a collection: backup metadata, DEL hash, FT.DROPINDEX (rollback HSET on error).
func (r *Repo) Delete(ctx context.Context, name string) error {
	metaKey := metaKey(name)

	// Backup metadata
	metaBackup, err := r.store.HGetAll(ctx, metaKey)
	if err != nil {
		return fmt.Errorf("hgetall collection %s: %w", name, err)
	}
	if len(metaBackup) == 0 {
		return domain.ErrNotFound
	}

	// Check index exists
	idxName := indexName(name)
	idxExists, err := r.store.IndexExists(ctx, idxName)
	if err != nil {
		return fmt.Errorf("check index exists: %w", err)
	}
	if !idxExists {
		return domain.ErrNotFound
	}

	// Step 1: DEL metadata
	if err := r.store.Del(ctx, metaKey); err != nil {
		return fmt.Errorf("del collection %s: %w", name, err)
	}

	// FT.DROPINDEX — rollback HSET on error
	if err := r.store.DropIndex(ctx, idxName); err != nil {
		cleanupErr := r.store.HSet(ctx, metaKey, metaBackup)
		return errors.Join(err, cleanupErr)
	}

	return nil
}

// Valkey key patterns: vecdex:collection:{name}, vecdex:{name}:idx, vecdex:{name}:

func metaKey(name string) string {
	return fmt.Sprintf("%scollection:%s", domain.KeyPrefix, name)
}

func indexName(name string) string {
	return fmt.Sprintf("%s%s:idx", domain.KeyPrefix, name)
}

func collectionPrefix(name string) string {
	return fmt.Sprintf("%s%s:", domain.KeyPrefix, name)
}
