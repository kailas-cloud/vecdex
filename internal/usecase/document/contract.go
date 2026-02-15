package document

import (
	"context"

	"github.com/kailas-cloud/vecdex/internal/domain"
	domcol "github.com/kailas-cloud/vecdex/internal/domain/collection"
	domdoc "github.com/kailas-cloud/vecdex/internal/domain/document"
	"github.com/kailas-cloud/vecdex/internal/domain/document/patch"
)

// Repository defines the storage contract for documents.
type Repository interface {
	Upsert(ctx context.Context, collectionName string, doc *domdoc.Document) (created bool, err error)
	Get(ctx context.Context, collectionName, id string) (domdoc.Document, error)
	List(ctx context.Context, collectionName, cursor string, limit int) (
		docs []domdoc.Document, nextCursor string, err error,
	)
	Delete(ctx context.Context, collectionName, id string) error
	Patch(ctx context.Context, collectionName, id string, p patch.Patch, newVector []float32) error
	Count(ctx context.Context, collectionName string) (int, error)
}

// CollectionReader reads collections for existence and schema validation.
type CollectionReader interface {
	Get(ctx context.Context, name string) (domcol.Collection, error)
}

// Embedder vectorizes text into embeddings.
type Embedder interface {
	Embed(ctx context.Context, text string) (domain.EmbeddingResult, error)
}
