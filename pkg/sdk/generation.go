package vecdex

import (
	"context"

	"github.com/kailas-cloud/vecdex/internal/domain"
)

// WithCollectionGeneration fences operations to a CollectionInfo.Generation.
// An empty generation clears the fence. A recreated collection is rejected.
func WithCollectionGeneration(ctx context.Context, generation string) context.Context {
	return domain.WithExpectedCollectionGeneration(ctx, generation)
}
