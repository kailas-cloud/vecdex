package collection

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"time"

	"github.com/kailas-cloud/vecdex/internal/db"
	"github.com/kailas-cloud/vecdex/internal/domain"
	domcol "github.com/kailas-cloud/vecdex/internal/domain/collection"
	"github.com/kailas-cloud/vecdex/internal/domain/storage"
)

//nolint:interfacebloat // lifecycle spans metadata, catalogs, work and index operations
type store interface {
	db.HashMutator
	db.CatalogStore
	db.PartitionCatalogStore
	db.MutationWorkStore
	HGetAll(context.Context, string) (map[string]string, error)
	HGetAllMulti(context.Context, []string) ([]map[string]string, error)
	Expire(context.Context, string, time.Duration, bool) error
	CreateIndex(context.Context, *db.IndexDefinition) error
	DropIndex(context.Context, string) error
	IndexExists(context.Context, string) (bool, error)
	WaitForIndex(context.Context, string) error
	SupportsTextSearch(context.Context) bool
}

// HNSWConfig contains index construction settings.
type HNSWConfig struct{ M, EFConstruct int }

// Repo stores recoverable collection lifecycle operations in control catalogs.
type Repo struct {
	store            store
	defaultVectorDim int
	hnsw             HNSWConfig
}

// New creates a collection repository.
func New(s store, dimensions int) *Repo {
	return &Repo{store: s, defaultVectorDim: dimensions, hnsw: HNSWConfig{M: 32, EFConstruct: 400}}
}

// WithHNSW overrides index construction defaults.
func (r *Repo) WithHNSW(cfg HNSWConfig) *Repo {
	if cfg.M > 0 {
		r.hnsw.M = cfg.M
	}
	if cfg.EFConstruct > 0 {
		r.hnsw.EFConstruct = cfg.EFConstruct
	}
	return r
}

// Create reserves a name and persists its recovery job before external index work.
// Cancellation leaves the registered operation for a background reconciler.
func (r *Repo) Create(ctx context.Context, col domcol.Collection) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	if _, err := storage.NewCollection(col.Name(), col.Generation()); err != nil {
		return lifecycleError("collection repo", err)
	}
	fields, err := collectionToHash(col)
	if err != nil {
		return lifecycleError("collection repo", err)
	}
	fields["state"], fields["layout_version"] = "creating", "3"
	fields["partition_count"] = strconv.Itoa(storage.PartitionCount)
	m := controlMutation(col.Name(), col.Generation(), "creating")
	m.Set, m.Replace, m.RequireAbsent = fields, true, true
	if _, err := r.store.MutateHash(ctx, m); err != nil {
		return fmt.Errorf("reserve collection: %w", err)
	}
	return r.finishCreate(ctx, col)
}

func controlMutation(name, generation, state string) *db.HashMutation {
	p := storage.ControlPartition(name)
	key := storage.Metadata(name)
	return &db.HashMutation{
		Key: key, StagingKey: key + ":pending", CatalogKey: storage.CollectionCatalog(p), CatalogMember: name,
		LifecycleKey: storage.LifecycleQueue(p), LifecycleMember: name, LifecyclePending: state != "active",
		Set: map[string]string{"state": state, "generation": generation},
	}
}

// Get returns active collections. Lifecycle recovery reads metadata directly.
func (r *Repo) Get(ctx context.Context, name string) (domcol.Collection, error) {
	row, err := r.store.HGetAll(ctx, storage.Metadata(name))
	if err != nil {
		return domcol.Collection{}, fmt.Errorf("read collection: %w", err)
	}
	if len(row) == 0 || row["state"] != "active" {
		return domcol.Collection{}, domain.ErrNotFound
	}
	return collectionFromHash(row, r.defaultVectorDim)
}

// List reads bounded control-catalog pages, never the backend keyspace.
func (r *Repo) List(ctx context.Context) ([]domcol.Collection, error) {
	catalogs := make([]string, storage.ControlPartitionCount)
	for p := range catalogs {
		catalogs[p] = storage.CollectionCatalog(p)
	}
	out := []domcol.Collection{}
	after := ""
	for {
		names, err := db.ReadCatalogPage(ctx, r.store, catalogs, after, 100)
		if err != nil {
			return nil, lifecycleError("collection repo", err)
		}
		if len(names) == 0 {
			break
		}
		keys := make([]string, len(names))
		for i, name := range names {
			keys[i] = storage.Metadata(name)
		}
		rows, err := r.store.HGetAllMulti(ctx, keys)
		if err != nil {
			return nil, fmt.Errorf("read collection page: %w", err)
		}
		if len(rows) != len(keys) {
			return nil, fmt.Errorf("collection page count mismatch")
		}
		for _, row := range rows {
			if row["state"] != "active" {
				continue
			}
			col, err := collectionFromHash(row, r.defaultVectorDim)
			if err != nil {
				return nil, lifecycleError("collection repo", err)
			}
			out = append(out, col)
		}
		after = names[len(names)-1]
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt() != out[j].CreatedAt() {
			return out[i].CreatedAt() < out[j].CreatedAt()
		}
		return out[i].Name() < out[j].Name()
	})
	return out, nil
}

// Delete establishes a metadata barrier and registers recoverable generation cleanup.
func (r *Repo) Delete(ctx context.Context, name string) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	row, err := r.store.HGetAll(ctx, storage.Metadata(name))
	if err != nil {
		return fmt.Errorf("read collection for deletion: %w", err)
	}
	if len(row) == 0 {
		return domain.ErrNotFound
	}
	generation := row["generation"]
	if err := domain.ValidateCollectionGeneration(ctx, generation); err != nil {
		return fmt.Errorf("delete collection generation: %w", err)
	}
	layout, err := storage.NewCollection(name, generation)
	if err != nil {
		return lifecycleError("collection repo", err)
	}
	present, err := r.markDeleting(ctx, name, generation, row)
	if err != nil {
		return err
	}
	if !present {
		return nil
	}
	return r.finishDelete(ctx, name, generation, layout)
}

func (r *Repo) markDeleting(ctx context.Context, name, generation string, row map[string]string) (bool, error) {
	for range 8 {
		if len(row) == 0 || row["generation"] != generation {
			return false, nil
		}
		if row["state"] == "deleting" {
			return true, nil
		}
		revision, err := strconv.Atoi(row["__revision"])
		if err != nil {
			return false, fmt.Errorf("read lifecycle revision: %w", err)
		}
		m := controlMutation(name, generation, "deleting")
		m.RequireExists, m.ExpectedRevision = true, &revision
		m.GuardKey, m.GuardGeneration = storage.Metadata(name), generation
		if _, err := r.store.MutateHash(ctx, m); err == nil {
			return true, nil
		} else if !errors.Is(err, domain.ErrRevisionConflict) &&
			!errors.Is(err, domain.ErrNotFound) && !errors.Is(err, domain.ErrDocumentNotFound) {
			return false, lifecycleError("mark collection deleting", err)
		}
		row, err = r.store.HGetAll(ctx, storage.Metadata(name))
		if err != nil {
			return false, lifecycleError("read concurrent lifecycle state", err)
		}
	}
	return false, fmt.Errorf("collection lifecycle changed repeatedly")
}

// These initial names are replaced with generation-bound names before FT.CREATE.
func indexName(name string) string        { return "vecdex:v3:index:" + name }
func collectionPrefix(name string) string { return "vecdex:v3:c:" + name + ":" }
