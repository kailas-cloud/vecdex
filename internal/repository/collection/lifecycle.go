package collection

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/kailas-cloud/vecdex/internal/db"
	"github.com/kailas-cloud/vecdex/internal/domain"
	domcol "github.com/kailas-cloud/vecdex/internal/domain/collection"
	"github.com/kailas-cloud/vecdex/internal/domain/storage"
)

func (r *Repo) finishCreate(ctx context.Context, col domcol.Collection) error {
	layout, err := storage.NewCollection(col.Name(), col.Generation())
	if err != nil {
		return lifecycleError("collection lifecycle", err)
	}
	index, err := buildIndex(col.Name(), col.Fields(), col.VectorDim(), r.store.SupportsTextSearch(ctx), r.hnsw)
	if err != nil {
		return lifecycleError("collection lifecycle", err)
	}
	index.Name, index.Prefixes = layout.Index(), []string{layout.ChunkPrefix()}
	exists, err := r.store.IndexExists(ctx, layout.Index())
	if err != nil {
		return lifecycleError("collection lifecycle", err)
	}
	if !exists {
		if err := r.store.CreateIndex(ctx, index); err != nil && !errors.Is(err, db.ErrIndexExists) {
			return lifecycleError("collection lifecycle", err)
		}
	}
	if err := r.store.WaitForIndex(ctx, layout.Index()); err != nil {
		return lifecycleError("collection index readiness", err)
	}
	for p := range storage.PartitionCount {
		if err := r.activateGuard(ctx, layout.Guard(p), col.Generation()); err != nil {
			return lifecycleError("collection lifecycle", err)
		}
	}
	m := controlMutation(col.Name(), col.Generation(), "active")
	m.RequireExists = true
	m.GuardKey, m.GuardGeneration, m.GuardState = storage.Metadata(col.Name()), col.Generation(), "creating"
	_, err = r.store.MutateHash(ctx, m)
	if errors.Is(err, domain.ErrNotFound) {
		current, getErr := r.Get(ctx, col.Name())
		if getErr == nil && current.Generation() == col.Generation() {
			return nil
		}
	}
	return lifecycleError("collection lifecycle", err)
}

func (r *Repo) activateGuard(ctx context.Context, key, generation string) error {
	// Metadata stays creating until every guard exists. Initializing a guard
	// active is safe; a concurrent delete's tombstone cannot be overwritten.
	_, err := r.store.MutateHash(ctx, &db.HashMutation{
		Key: key, StagingKey: key + ":pending", Set: map[string]string{"generation": generation, "state": "active"},
		RequireAbsent: true, Replace: true,
	})
	if !errors.Is(err, domain.ErrAlreadyExists) {
		return lifecycleError("collection lifecycle", err)
	}
	row, err := r.store.HGetAll(ctx, key)
	if err != nil {
		return lifecycleError("collection lifecycle", err)
	}
	if row["generation"] == generation && row["state"] == "active" {
		return nil
	}
	return domain.ErrNotFound
}

func (r *Repo) closeGuard(ctx context.Context, key, generation string) error {
	return r.advanceGuard(ctx, key, generation, "deleting")
}

func (r *Repo) advanceGuard(ctx context.Context, key, generation, state string) error {
	for range 8 {
		row, err := r.store.HGetAll(ctx, key)
		if err != nil {
			return lifecycleError("read partition guard", err)
		}
		if len(row) > 0 && row["generation"] != generation {
			return domain.ErrNotFound
		}
		if row["state"] == state || row["state"] == "purging" {
			return nil
		}
		m := &db.HashMutation{
			Key:        key,
			StagingKey: key + ":pending",
			Set:        map[string]string{"generation": generation, "state": state},
		}
		if len(row) == 0 {
			m.RequireAbsent = true
			m.Replace = true
		} else {
			m.GuardKey, m.GuardGeneration, m.GuardState = key, generation, row["state"]
		}
		if _, err := r.store.MutateHash(ctx, m); err == nil {
			return nil
		} else if !errors.Is(err, domain.ErrAlreadyExists) && !errors.Is(err, domain.ErrNotFound) {
			return lifecycleError("advance partition guard", err)
		}
	}
	return fmt.Errorf("partition guard changed repeatedly")
}

func (r *Repo) finishDelete(ctx context.Context, name, generation string, layout storage.Collection) error {
	// No cleanup starts until every partition rejects old prepare/commit attempts.
	for p := range storage.PartitionCount {
		if err := r.closeGuard(ctx, layout.Guard(p), generation); err != nil {
			return lifecycleError("collection lifecycle", err)
		}
	}
	for p := range storage.PartitionCount {
		if err := r.drainPartition(ctx, layout, generation, p); err != nil {
			return lifecycleError("collection lifecycle", err)
		}
	}
	// Deleting permits lifecycle workers to remove parents. Purging closes
	// registration for those workers too before the final queue drain.
	for p := range storage.PartitionCount {
		if err := r.advanceGuard(ctx, layout.Guard(p), generation, "purging"); err != nil {
			return err
		}
	}
	for p := range storage.PartitionCount {
		if err := r.cancelPartitionWork(ctx, layout.WorkQueue(p)); err != nil {
			return err
		}
	}
	if err := r.store.DropIndex(ctx, layout.Index()); err != nil && !errors.Is(err, db.ErrIndexNotFound) {
		return lifecycleError("collection lifecycle", err)
	}
	// Guards only fence bounded in-flight requests; missing guards also reject writes.
	// Keep tombstones long enough to stop late creators before reclaiming them.
	for p := range storage.PartitionCount {
		if err := r.store.Expire(ctx, layout.Guard(p), 24*time.Hour, false); err != nil {
			return lifecycleError("collection lifecycle", err)
		}
	}
	m := controlMutation(name, generation, "active")
	m.Delete, m.Set = true, nil
	m.GuardKey, m.GuardGeneration, m.GuardState = storage.Metadata(name), generation, "deleting"
	_, err := r.store.MutateHash(ctx, m)
	if errors.Is(err, domain.ErrNotFound) {
		return nil
	}
	return lifecycleError("collection lifecycle", err)
}

func (r *Repo) drainPartition(ctx context.Context, layout storage.Collection, generation string, p int) error {
	for {
		ids, err := r.store.CatalogIDs(ctx, layout.Catalog(p), "", 64)
		if err != nil {
			return lifecycleError("collection lifecycle", err)
		}
		if len(ids) == 0 {
			break
		}
		for _, id := range ids {
			_, err := r.store.MutateHash(ctx, &db.HashMutation{
				Key: layout.Document(id), StagingKey: layout.Staging(id, "delete"), Delete: true,
				CatalogKey: layout.Catalog(p), CatalogMember: id, WorkQueueKey: layout.WorkQueue(p),
				DeadlineKey:     layout.Deadlines(p),
				ExpiryMarkerKey: layout.ExpiryMarker(id),
				GuardKey:        layout.Guard(p), GuardGeneration: generation, GuardState: "deleting",
			})
			if err := r.resolveConcurrentDeletion(ctx, layout.Document(id), err); err != nil {
				return err
			}
		}
	}
	return nil
}

func (r *Repo) resolveConcurrentDeletion(ctx context.Context, key string, writeErr error) error {
	if writeErr == nil {
		return nil
	}
	if !errors.Is(writeErr, db.ErrMutationFenced) && !errors.Is(writeErr, domain.ErrNotFound) &&
		!errors.Is(writeErr, domain.ErrRevisionConflict) {
		return lifecycleError("delete partition source", writeErr)
	}
	row, err := r.store.HGetAll(ctx, key)
	if err != nil {
		return lifecycleError("verify concurrent deletion", err)
	}
	if len(row) == 0 {
		return nil
	}
	return lifecycleError("delete partition source", writeErr)
}

func (r *Repo) cancelPartitionWork(ctx context.Context, queue string) error {
	for {
		count, err := r.store.CatalogCount(ctx, queue)
		if err != nil || count == 0 {
			return lifecycleError("collection lifecycle", err)
		}
		if _, err := r.store.CancelMutations(ctx, queue, 64); err != nil {
			return lifecycleError("collection lifecycle", err)
		}
	}
}

// RecoverLifecycle resumes registered create/delete work without a keyspace scan.
func (r *Repo) RecoverLifecycle(ctx context.Context, after string, limit int) (string, error) {
	queues := make([]string, storage.ControlPartitionCount)
	for p := range queues {
		queues[p] = storage.LifecycleQueue(p)
	}
	names, err := db.ReadCatalogPage(ctx, r.store, queues, after, limit)
	if err != nil {
		return after, lifecycleError("collection lifecycle", err)
	}
	if len(names) == 0 {
		return "", nil
	}
	var failures []error
	for _, name := range names {
		if err := r.recoverLifecycleName(ctx, name); err != nil {
			failures = append(failures, err)
		}
		after = name
	}
	return after, errors.Join(failures...)
}

func (r *Repo) recoverLifecycleName(ctx context.Context, name string) error {
	row, err := r.store.HGetAll(ctx, storage.Metadata(name))
	if err != nil {
		return lifecycleError("collection lifecycle", err)
	}
	switch row["state"] {
	case "creating":
		col, err := collectionFromHash(row, r.defaultVectorDim)
		if err != nil {
			return lifecycleError("collection lifecycle", err)
		}
		return r.finishCreate(ctx, col)
	case "deleting":
		err := r.Delete(ctx, name)
		if errors.Is(err, domain.ErrNotFound) {
			return nil
		}
		return lifecycleError("collection lifecycle", err)
	case "active", "":
		// A concurrent worker may already have completed or removed the record.
		return nil
	default:
		return fmt.Errorf("invalid lifecycle state %q for %s", row["state"], name)
	}
}

func lifecycleError(operation string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", operation, err)
}
