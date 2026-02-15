package batch

import (
	"context"

	"github.com/kailas-cloud/vecdex/internal/domain"
	domcol "github.com/kailas-cloud/vecdex/internal/domain/collection"
	domdoc "github.com/kailas-cloud/vecdex/internal/domain/document"
)

// DocumentUpserter creates or updates a document in storage.
type DocumentUpserter interface {
	Upsert(ctx context.Context, collectionName string, doc *domdoc.Document) (created bool, err error)
}

// DocumentDeleter deletes a document from storage.
type DocumentDeleter interface {
	Delete(ctx context.Context, collectionName, id string) error
}

// CollectionReader reads collections for existence checks.
type CollectionReader interface {
	Get(ctx context.Context, name string) (domcol.Collection, error)
}

// Embedder vectorizes text into embeddings.
type Embedder interface {
	Embed(ctx context.Context, text string) (domain.EmbeddingResult, error)
}
