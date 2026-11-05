package collection

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/kailas-cloud/vecdex/internal/db"
	"github.com/kailas-cloud/vecdex/internal/domain"
	domdoc "github.com/kailas-cloud/vecdex/internal/domain/document"
	"github.com/kailas-cloud/vecdex/internal/domain/storage"
)

func (r *Repo) expirePartitionBlock(
	ctx context.Context, layout storage.Collection, generation string, start int,
) error {
	backend, ok := r.store.(db.ExpiryStore)
	if !ok {
		return nil
	}
	keys := make([]string, maintenancePartitionBlock)
	for i := range keys {
		keys[i] = layout.Deadlines((start + i) % storage.PartitionCount)
	}
	now := time.Now().UnixMilli()
	pages, err := backend.ExpiryIDPages(ctx, keys, now, 8)
	if err != nil {
		return fmt.Errorf("discover expired sources: %w", err)
	}
	if len(pages) != len(keys) {
		return fmt.Errorf("expiry window response count mismatch")
	}
	var failures []error
	for _, ids := range pages {
		for _, id := range ids {
			if err := r.expireSource(ctx, layout, generation, id, now); err != nil {
				failures = append(failures, err)
			}
		}
	}
	return errors.Join(failures...)
}

func (r *Repo) expireSource(ctx context.Context, layout storage.Collection, generation, id string, now int64) error {
	row, err := r.store.HGetAll(ctx, layout.Document(id))
	if err != nil {
		return fmt.Errorf("read expired source: %w", err)
	}
	expiresAt, err := domdoc.ParseExpiresAt(row["__expires_at"])
	if err != nil {
		return fmt.Errorf("read source deadline: %w", err)
	}
	if len(row) > 0 && !domdoc.Expired(expiresAt, now) {
		return nil // A replacement or lease extension won before this worker read it.
	}
	p := storage.Partition(id)
	m := &db.HashMutation{Key: layout.Document(id), Delete: true,
		ExpiryMarkerKey: layout.ExpiryMarker(id),
		CatalogKey:      layout.Catalog(p), CatalogMember: id, DeadlineKey: layout.Deadlines(p),
		WorkQueueKey: layout.WorkQueue(p), GuardKey: layout.Guard(p), GuardGeneration: generation, GuardState: "active"}
	if len(row) == 0 {
		m.RequireAbsent = true // Repair a dangling entry without deleting a concurrent new source.
	} else {
		version := row["__source_version"]
		if version == "" {
			return fmt.Errorf("expired source has no committed version")
		}
		m.ExpectedVersion, m.ExpiredBefore = &version, now
	}
	_, err = r.store.MutateHash(ctx, m)
	if errors.Is(err, domain.ErrRevisionConflict) || errors.Is(err, domain.ErrAlreadyExists) ||
		errors.Is(err, domain.ErrNotFound) {
		return nil // A writer or collection lifecycle worker won the fenced transaction.
	}
	if err != nil {
		return fmt.Errorf("expire source: %w", err)
	}
	return nil
}
