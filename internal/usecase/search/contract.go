package search

import (
	"context"

	"github.com/kailas-cloud/vecdex/internal/domain"
	domcol "github.com/kailas-cloud/vecdex/internal/domain/collection"
	domdoc "github.com/kailas-cloud/vecdex/internal/domain/document"
	"github.com/kailas-cloud/vecdex/internal/domain/search/filter"
	"github.com/kailas-cloud/vecdex/internal/domain/search/result"
)

// Repository defines the storage contract for search operations.
type Repository interface {
	SearchKNN(
		ctx context.Context, collectionName string,
		vector []float32, filters filter.Expression, topK int,
		includeVectors bool, rawScores bool,
	) ([]result.Result, error)

	SearchBM25(
		ctx context.Context, collectionName string,
		query string, filters filter.Expression, topK int,
	) ([]result.Result, error)

	SupportsTextSearch(ctx context.Context) bool
}

// CollectionReader reads collections for existence checks.
type CollectionReader interface {
	Get(ctx context.Context, name string) (domcol.Collection, error)
}

// DocumentReader reads documents for vector retrieval (used by Similar).
type DocumentReader interface {
	Get(ctx context.Context, collectionName, id string) (domdoc.Document, error)
}

// Embedder vectorizes text into embeddings.
type Embedder interface {
	Embed(ctx context.Context, text string) (domain.EmbeddingResult, error)
}
