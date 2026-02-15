package batch

import (
	"context"
	"errors"
	"fmt"

	"github.com/kailas-cloud/vecdex/internal/domain"
	dombatch "github.com/kailas-cloud/vecdex/internal/domain/batch"
	"github.com/kailas-cloud/vecdex/internal/domain/collection/field"
	domdoc "github.com/kailas-cloud/vecdex/internal/domain/document"
)

// MaxBatchSize is the maximum number of items per batch request.
const MaxBatchSize = 100

// Service handles batch document operations with per-item error reporting.
type Service struct {
	docs         DocumentUpserter
	del          DocumentDeleter
	colls        CollectionReader
	embed        Embedder
	maxBatchSize int
}

// New creates a batch service.
func New(docs DocumentUpserter, del DocumentDeleter, colls CollectionReader, embed Embedder) *Service {
	return &Service{docs: docs, del: del, colls: colls, embed: embed, maxBatchSize: MaxBatchSize}
}

// WithMaxBatchSize configures the maximum batch size.
func (s *Service) WithMaxBatchSize(size int) *Service {
	if size > 0 {
		s.maxBatchSize = size
	}
	return s
}

// Upsert creates or updates documents in batch.
func (s *Service) Upsert(ctx context.Context, collectionName string, items []domdoc.Document) []dombatch.Result {
	results := make([]dombatch.Result, len(items))

	if len(items) > s.maxBatchSize {
		for i, item := range items {
			results[i] = dombatch.NewError(
				item.ID(),
				fmt.Errorf("batch size exceeds %d: %w", s.maxBatchSize, domain.ErrInvalidSchema),
			)
		}
		return results
	}

	col, err := s.colls.Get(ctx, collectionName)
	if err != nil {
		for i, item := range items {
			results[i] = dombatch.NewError(item.ID(), fmt.Errorf("get collection: %w", err))
		}
		return results
	}

	fieldTypes := make(map[string]field.Type)
	for _, f := range col.Fields() {
		fieldTypes[f.Name()] = f.FieldType()
	}

	for i, item := range items {
		if err := validateItemFields(&item, fieldTypes); err != nil {
			results[i] = dombatch.NewError(item.ID(), err)
			continue
		}

		embResult, err := s.embed.Embed(ctx, item.Content())
		if err != nil {
			// Quota/rate-limit errors cascade: skip all remaining items
			if errors.Is(err, domain.ErrEmbeddingQuotaExceeded) || errors.Is(err, domain.ErrRateLimited) {
				results[i] = dombatch.NewError(item.ID(), err)
				for j := i + 1; j < len(items); j++ {
					results[j] = dombatch.NewError(items[j].ID(), err)
				}
				return results
			}
			results[i] = dombatch.NewError(item.ID(), fmt.Errorf("vectorize: %w", err))
			continue
		}

		domain.UsageFromContext(ctx).AddTokens(embResult.TotalTokens)

		item.SetVector(embResult.Embedding)
		if _, err := s.docs.Upsert(ctx, collectionName, &item); err != nil {
			results[i] = dombatch.NewError(item.ID(), fmt.Errorf("upsert: %w", err))
			continue
		}

		results[i] = dombatch.NewOK(item.ID())
	}

	return results
}

// Delete removes documents by ID in batch.
func (s *Service) Delete(ctx context.Context, collectionName string, ids []string) []dombatch.Result {
	results := make([]dombatch.Result, len(ids))

	if len(ids) > s.maxBatchSize {
		for i, id := range ids {
			results[i] = dombatch.NewError(id, fmt.Errorf("batch size exceeds %d: %w", s.maxBatchSize, domain.ErrInvalidSchema))
		}
		return results
	}

	if _, err := s.colls.Get(ctx, collectionName); err != nil {
		for i, id := range ids {
			results[i] = dombatch.NewError(id, fmt.Errorf("get collection: %w", err))
		}
		return results
	}

	for i, id := range ids {
		if err := s.del.Delete(ctx, collectionName, id); err != nil {
			results[i] = dombatch.NewError(id, fmt.Errorf("delete: %w", err))
			continue
		}
		results[i] = dombatch.NewOK(id)
	}

	return results
}

func validateItemFields(doc *domdoc.Document, fieldTypes map[string]field.Type) error {
	for k := range doc.Tags() {
		ft, ok := fieldTypes[k]
		if !ok {
			return fmt.Errorf("unknown field %q: %w", k, domain.ErrInvalidSchema)
		}
		if ft != field.Tag {
			return fmt.Errorf("field %q is %s, not tag: %w", k, ft, domain.ErrInvalidSchema)
		}
	}
	for k := range doc.Numerics() {
		ft, ok := fieldTypes[k]
		if !ok {
			return fmt.Errorf("unknown field %q: %w", k, domain.ErrInvalidSchema)
		}
		if ft != field.Numeric {
			return fmt.Errorf("field %q is %s, not numeric: %w", k, ft, domain.ErrInvalidSchema)
		}
	}
	return nil
}
