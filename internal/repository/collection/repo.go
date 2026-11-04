package collection

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"

	"github.com/kailas-cloud/vecdex/internal/db"
	"github.com/kailas-cloud/vecdex/internal/domain"
	domcol "github.com/kailas-cloud/vecdex/internal/domain/collection"
)

// store is the consumer interface for collections (ISP).
//
//nolint:interfacebloat // collection repo needs hash + index management operations
type store interface {
	db.HashMutator
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

	// Prepare index definition and hash data before writes
	indexDef, err := buildIndex(name, col.Fields(), col.VectorDim(), r.store.SupportsTextSearch(ctx), r.hnsw)
	if err != nil {
		return fmt.Errorf("build index: %w", err)
	}
	hashData, err := collectionToHash(col)
	if err != nil {
		return err
	}

	indexDef.Name = domain.GenerationIndexName(name, col.Generation())
	indexDef.Prefixes = []string{domain.CollectionDataBase(name, col.Generation()) + ":chunk:"}
	hashData["state"] = "creating"
	reserved, err := r.store.MutateHash(ctx, &db.HashMutation{
		Key: metaKey(
			name,
		),
		StagingKey:    domain.CollectionBase(name) + ":meta-pending",
		Set:           hashData,
		Replace:       true,
		RequireAbsent: true,
	})
	if err != nil {
		return fmt.Errorf("reserve collection: %w", err)
	}
	if err := r.store.CreateIndex(ctx, indexDef); err != nil {
		return r.rollbackCreate(ctx, name, col.Generation(), reserved.Revision, err)
	}
	_, err = r.store.MutateHash(ctx, &db.HashMutation{
		Key: metaKey(
			name,
		),
		StagingKey:       domain.CollectionBase(name) + ":meta-pending",
		Set:              map[string]string{"state": "active"},
		RequireExists:    true,
		ExpectedRevision: &reserved.Revision,
		GuardKey:         metaKey(name),
		GuardGeneration:  col.Generation(),
		GuardState:       "creating",
	})
	if err != nil {
		return r.rollbackCreate(ctx, name, col.Generation(), reserved.Revision, err)
	}
	return nil
}

// Get retrieves a collection by name.
func (r *Repo) Get(ctx context.Context, name string) (domcol.Collection, error) {
	m, err := r.store.HGetAll(ctx, metaKey(name))
	if err != nil {
		return domcol.Collection{}, fmt.Errorf("hgetall collection %s: %w", name, err)
	}
	if len(m) == 0 || (m["state"] != "" && m["state"] != "active") {
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
		if len(m) == 0 || (m["state"] != "" && m["state"] != "active") {
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
	metadata, err := r.store.HGetAll(ctx, metaKey(name))
	if err != nil {
		return fmt.Errorf("read collection for deletion: %w", err)
	}
	if len(metadata) == 0 {
		return domain.ErrNotFound
	}
	generation := metadata["generation"]
	if generation == "" {
		return fmt.Errorf("collection generation is missing: %w", domain.ErrInvalidSchema)
	}
	if metadata["state"] != "deleting" {
		revision, _ := strconv.Atoi(metadata["__revision"])
		_, err = r.store.MutateHash(ctx, &db.HashMutation{
			Key: metaKey(
				name,
			),
			StagingKey:       domain.CollectionBase(name) + ":meta-pending",
			Set:              map[string]string{"state": "deleting"},
			RequireExists:    true,
			ExpectedRevision: &revision,
			GuardKey:         metaKey(name),
			GuardGeneration:  generation,
		})
		if err != nil {
			return fmt.Errorf("mark collection deleting: %w", err)
		}
	}
	keys, err := r.store.Scan(ctx, domain.CollectionDataBase(name, generation)+":*")
	if err != nil {
		return fmt.Errorf("scan collection data: %w", err)
	}
	for _, key := range keys {
		if key == metaKey(name) || key == domain.CollectionBase(name)+":meta-pending" {
			continue
		}
		if err := r.store.Del(ctx, key); err != nil {
			return fmt.Errorf("delete collection data: %w", err)
		}
	}
	if err := r.store.DropIndex(
		ctx,
		domain.GenerationIndexName(name, generation),
	); err != nil &&
		!errors.Is(err, db.ErrIndexNotFound) {
		return fmt.Errorf("drop collection index: %w", err)
	}
	_, err = r.store.MutateHash(ctx, &db.HashMutation{
		Key: metaKey(name), StagingKey: domain.CollectionBase(name) + ":meta-pending", Delete: true,
		GuardKey: metaKey(name), GuardGeneration: generation, GuardState: "deleting",
	})
	if errors.Is(err, domain.ErrNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("finish collection deletion: %w", err)
	}
	return nil
}

// Valkey key patterns: vecdex:collection:{name}, vecdex:{name}:idx, vecdex:{name}:

func metaKey(name string) string {
	return domain.CollectionMetadataKey(name)
}

func indexName(name string) string {
	return domain.CollectionIndexName(name)
}

func collectionPrefix(name string) string {
	return domain.DocumentPrefix(name)
}

func (r *Repo) rollbackCreate(ctx context.Context, name, generation string, revision int, cause error) error {
	dropErr := r.store.DropIndex(ctx, domain.GenerationIndexName(name, generation))
	if errors.Is(dropErr, db.ErrIndexNotFound) {
		dropErr = nil
	}
	_, cleanupErr := r.store.MutateHash(ctx, &db.HashMutation{
		Key: metaKey(name), StagingKey: domain.CollectionBase(name) + ":meta-pending", Delete: true,
		ExpectedRevision: &revision, GuardKey: metaKey(name), GuardGeneration: generation, GuardState: "creating",
	})
	return errors.Join(cause, dropErr, cleanupErr)
}
