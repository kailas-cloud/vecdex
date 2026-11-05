package document

import (
	"context"
	"crypto/rand"
	"fmt"

	"github.com/kailas-cloud/vecdex/internal/db"
	"github.com/kailas-cloud/vecdex/internal/domain"
	domdoc "github.com/kailas-cloud/vecdex/internal/domain/document"
	"github.com/kailas-cloud/vecdex/internal/domain/storage"
)

// BeginWrite reserves a request before embedding, or returns its compact outcome.
func (r *Repo) BeginWrite(
	ctx context.Context, collection, id, fingerprint string,
) (context.Context, *domdoc.WriteResult, error) {
	backend, ok := r.store.(db.WriteRequestStore)
	if !ok {
		return ctx, nil, domain.ErrNotImplemented
	}
	layout, err := storage.NewCollection(collection, domain.CollectionGeneration(ctx))
	if err != nil {
		return ctx, nil, fmt.Errorf("idempotency layout: %w", err)
	}
	request := &db.WriteRequest{Key: layout.Request(id, domain.IdempotencyKey(ctx)),
		Owner: rand.Text(), Fingerprint: fingerprint, GuardKey: layout.Guard(storage.Partition(id)),
		GuardGeneration: domain.CollectionGeneration(ctx)}
	result, err := backend.ClaimWriteRequest(ctx, request)
	if err != nil {
		return ctx, nil, fmt.Errorf("claim source request: %w", err)
	}
	if result.Replay {
		return ctx, &domdoc.WriteResult{ID: id, Version: result.Result.Version, Created: result.Result.Created}, nil
	}
	ctx = domain.WithWriteClaim(ctx, domain.WriteClaim{
		Key: request.Key, Owner: request.Owner, Fingerprint: request.Fingerprint,
	})
	return ctx, nil, nil
}

// ReleaseWrite fences an unfinished attempt; committed receipts are retained.
func (r *Repo) ReleaseWrite(ctx context.Context) error {
	claim, ok := domain.CurrentWriteClaim(ctx)
	if !ok {
		return nil
	}
	backend, ok := r.store.(db.WriteRequestStore)
	if !ok {
		return domain.ErrNotImplemented
	}
	if err := backend.ReleaseWriteRequest(ctx, &db.WriteRequest{Key: claim.Key, Owner: claim.Owner}); err != nil {
		return fmt.Errorf("release source request: %w", err)
	}
	return nil
}

func requestForMutation(ctx context.Context) *db.WriteRequest {
	claim, ok := domain.CurrentWriteClaim(ctx)
	if !ok {
		return nil
	}
	return &db.WriteRequest{Key: claim.Key, Owner: claim.Owner, Fingerprint: claim.Fingerprint}
}
