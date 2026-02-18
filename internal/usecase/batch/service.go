package batch

import (
	"context"
	"errors"
	"fmt"

	"github.com/kailas-cloud/vecdex/internal/domain"
	dombatch "github.com/kailas-cloud/vecdex/internal/domain/batch"
	"github.com/kailas-cloud/vecdex/internal/domain/collection/field"
	domdoc "github.com/kailas-cloud/vecdex/internal/domain/document"
	"github.com/kailas-cloud/vecdex/internal/domain/geo"
)

// MaxBatchSize is the maximum number of items per batch request.
const MaxBatchSize = 100

// Service handles batch document operations with per-item error reporting.
type Service struct {
	docs         DocumentUpserter
	batchDocs    BulkUpserter
	del          DocumentDeleter
	colls        CollectionReader
	embed        Embedder
	maxBatchSize int
}

// New creates a batch service.
func New(
	docs DocumentUpserter, batchDocs BulkUpserter,
	del DocumentDeleter, colls CollectionReader, embed Embedder,
) *Service {
	return &Service{
		docs: docs, batchDocs: batchDocs,
		del: del, colls: colls, embed: embed,
		maxBatchSize: MaxBatchSize,
	}
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

	if col.IsGeo() {
		return s.upsertGeoBatch(ctx, collectionName, items, fieldTypes)
	}
	return s.upsertTextBatch(ctx, collectionName, items, fieldTypes)
}

// upsertGeoBatch validates, vectorizes, and stores all geo docs in a single pipeline.
func (s *Service) upsertGeoBatch(
	ctx context.Context,
	collectionName string,
	items []domdoc.Document,
	fieldTypes map[string]field.Type,
) []dombatch.Result {
	results := make([]dombatch.Result, len(items))

	// Validate and vectorize all items; collect valid ones for batch upsert.
	valid := make([]domdoc.Document, 0, len(items))
	validIdx := make([]int, 0, len(items))

	for i := range items {
		if err := validateItemFields(&items[i], fieldTypes); err != nil {
			results[i] = dombatch.NewError(items[i].ID(), err)
			continue
		}
		if err := vectorizeGeo(&items[i]); err != nil {
			results[i] = dombatch.NewError(items[i].ID(), err)
			continue
		}
		valid = append(valid, items[i])
		validIdx = append(validIdx, i)
	}

	if len(valid) == 0 {
		return results
	}

	if err := s.batchDocs.BatchUpsert(ctx, collectionName, valid); err != nil {
		for _, i := range validIdx {
			results[i] = dombatch.NewError(items[i].ID(), fmt.Errorf("batch upsert: %w", err))
		}
		return results
	}

	for _, i := range validIdx {
		results[i] = dombatch.NewOK(items[i].ID())
	}
	return results
}

// upsertTextBatch processes text documents one-by-one (embeddings are per-doc).
func (s *Service) upsertTextBatch(
	ctx context.Context,
	collectionName string,
	items []domdoc.Document,
	fieldTypes map[string]field.Type,
) []dombatch.Result {
	results := make([]dombatch.Result, len(items))

	for i, item := range items {
		if err := validateItemFields(&item, fieldTypes); err != nil {
			results[i] = dombatch.NewError(item.ID(), err)
			continue
		}

		cascade, err := s.vectorizeText(ctx, &item)
		if err != nil {
			if cascade {
				results[i] = dombatch.NewError(item.ID(), err)
				for j := i + 1; j < len(items); j++ {
					results[j] = dombatch.NewError(items[j].ID(), err)
				}
				return results
			}
			results[i] = dombatch.NewError(item.ID(), err)
			continue
		}

		if _, err := s.docs.Upsert(ctx, collectionName, &item); err != nil {
			results[i] = dombatch.NewError(item.ID(), fmt.Errorf("upsert: %w", err))
			continue
		}

		results[i] = dombatch.NewOK(item.ID())
	}

	return results
}

// vectorizeText embeds document content via the embedding API.
// Returns (cascade, error): cascade=true means quota/rate-limit error, skip remaining.
func (s *Service) vectorizeText(
	ctx context.Context, item *domdoc.Document,
) (bool, error) {
	embResult, err := s.embed.Embed(ctx, item.Content())
	if err != nil {
		cascade := errors.Is(err, domain.ErrEmbeddingQuotaExceeded) ||
			errors.Is(err, domain.ErrRateLimited)
		return cascade, fmt.Errorf("vectorize: %w", err)
	}
	domain.UsageFromContext(ctx).AddTokens(embResult.TotalTokens)
	item.SetVector(embResult.Embedding)
	return false, nil
}

// vectorizeGeo sets ECEF vector from latitude/longitude numerics.
func vectorizeGeo(item *domdoc.Document) error {
	lat, hasLat := item.Numerics()["latitude"]
	lon, hasLon := item.Numerics()["longitude"]
	if !hasLat || !hasLon {
		return fmt.Errorf(
			"geo document requires latitude and longitude numerics: %w",
			domain.ErrInvalidSchema,
		)
	}
	if !geo.ValidateCoordinates(lat, lon) {
		return fmt.Errorf(
			"invalid coordinates: lat=%f lon=%f: %w",
			lat, lon, domain.ErrGeoQueryInvalid,
		)
	}
	item.SetVector(geo.ToVector(lat, lon))
	return nil
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
