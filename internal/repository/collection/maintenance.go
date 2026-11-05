package collection

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/kailas-cloud/vecdex/internal/db"
	"github.com/kailas-cloud/vecdex/internal/domain/storage"
)

const maintenancePartitionBlock = 32

// StartMaintenance runs bounded recovery passes and returns an idempotent stop
// function. Stop waits for the worker before the caller closes the database.
func (r *Repo) StartMaintenance(parent context.Context, report func(error)) func() {
	ctx, cancel := context.WithCancel(parent)
	done := make(chan struct{})
	go func() { defer close(done); r.runMaintenance(ctx, report) }()
	var once sync.Once
	return func() { once.Do(func() { cancel(); <-done }) }
}

func (r *Repo) runMaintenance(ctx context.Context, report func(error)) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	lifecycleAfter, documentAfter, partition := "", "", 0
	for {
		pass, cancel := context.WithTimeout(ctx, 10*time.Second)
		next, err := r.RecoverLifecycle(pass, lifecycleAfter, 8)
		lifecycleAfter = next
		if err != nil && report != nil && ctx.Err() == nil {
			report(fmt.Errorf("recover collection lifecycle: %w", err))
		}
		next, err = r.recoverDocumentQueues(pass, documentAfter, partition)
		documentAfter = next
		if documentAfter == "" {
			partition = (partition + maintenancePartitionBlock) % storage.PartitionCount
		}
		if err != nil && report != nil && ctx.Err() == nil {
			report(fmt.Errorf("recover document work: %w", err))
		}
		cancel()
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (r *Repo) recoverDocumentQueues(ctx context.Context, after string, partition int) (string, error) {
	catalogs := make([]string, storage.ControlPartitionCount)
	for p := range catalogs {
		catalogs[p] = storage.CollectionCatalog(p)
	}
	names, err := db.ReadCatalogPage(ctx, r.store, catalogs, after, 8)
	if err != nil {
		return after, lifecycleError("collection maintenance", err)
	}
	if len(names) == 0 {
		return "", nil
	}
	var failures []error
	for _, name := range names {
		row, err := r.store.HGetAll(ctx, storage.Metadata(name))
		if err != nil {
			failures = append(failures, err)
			continue
		}
		if row["state"] != "active" {
			continue
		}
		layout, err := storage.NewCollection(name, row["generation"])
		if err != nil {
			failures = append(failures, err)
			continue
		}
		if err := r.recoverPartitionBlock(ctx, layout, row["generation"], partition); err != nil {
			failures = append(failures, err)
		}
	}
	return names[len(names)-1], errors.Join(failures...)
}

func (r *Repo) recoverPartitionBlock(
	ctx context.Context, layout storage.Collection, generation string, start int,
) error {
	keys := make([]string, maintenancePartitionBlock)
	for i := range keys {
		keys[i] = layout.WorkQueue((start + i) % storage.PartitionCount)
	}
	counts, err := r.store.CatalogCounts(ctx, keys)
	if err != nil {
		return lifecycleError("collection maintenance", err)
	}
	if len(counts) != len(keys) {
		return fmt.Errorf("work queue count response mismatch")
	}
	var failures []error
	for i, count := range counts {
		if count == 0 {
			continue
		}
		if _, err := r.store.RecoverMutations(ctx, keys[i], 8); err != nil {
			failures = append(failures, err)
		}
	}
	failures = append(failures, r.expirePartitionBlock(ctx, layout, generation, start))
	return errors.Join(failures...)
}
