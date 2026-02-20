package batch

import (
	"context"
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
	batchEmbed   BulkEmbedder
	maxBatchSize int
}

// New creates a batch service. batchEmbed может быть nil — тогда fallback на поштучный Embed.
func New(
	docs DocumentUpserter, batchDocs BulkUpserter,
	del DocumentDeleter, colls CollectionReader, embed Embedder,
	batchEmbed BulkEmbedder,
) *Service {
	return &Service{
		docs: docs, batchDocs: batchDocs,
		del: del, colls: colls, embed: embed,
		batchEmbed:   batchEmbed,
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

// upsertTextBatch validates all docs first, then batch-embeds, then bulk upserts.
func (s *Service) upsertTextBatch(
	ctx context.Context,
	collectionName string,
	items []domdoc.Document,
	fieldTypes map[string]field.Type,
) []dombatch.Result {
	results := make([]dombatch.Result, len(items))

	// Фаза 1: валидация — отсеиваем невалидные ДО эмбеддинга
	var validItems []domdoc.Document
	var validIdx []int

	for i := range items {
		if err := validateItemFields(&items[i], fieldTypes); err != nil {
			results[i] = dombatch.NewError(items[i].ID(), err)
			continue
		}
		validItems = append(validItems, items[i])
		validIdx = append(validIdx, i)
	}

	if len(validItems) == 0 {
		return results
	}

	// Фаза 2: batch embed
	texts := make([]string, len(validItems))
	for j, item := range validItems {
		texts[j] = item.Content()
	}

	embResult, err := s.doBatchEmbed(ctx, texts)
	if err != nil {
		// Каскадная ошибка — все валидные фейлятся
		embErr := fmt.Errorf("vectorize: %w", err)
		for _, i := range validIdx {
			results[i] = dombatch.NewError(items[i].ID(), embErr)
		}
		return results
	}

	// Раздаём вектора по документам
	for j, idx := range validIdx {
		items[idx].SetVector(embResult.Embeddings[j])
		validItems[j] = items[idx]
	}
	domain.UsageFromContext(ctx).AddTokens(embResult.TotalTokens)

	// Фаза 3: single pipeline upsert.
	// Атомарная семантика: при ошибке все элементы фейлятся. Часть может быть уже записана
	// в БД (pipeline partial write) — клиент должен быть готов к идемпотентному retry.
	if err := s.batchDocs.BatchUpsert(ctx, collectionName, validItems); err != nil {
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

// doBatchEmbed использует BulkEmbedder если доступен, иначе fallback на поштучный Embed.
func (s *Service) doBatchEmbed(ctx context.Context, texts []string) (domain.BatchEmbeddingResult, error) {
	if s.batchEmbed != nil {
		res, err := s.batchEmbed.BatchEmbed(ctx, texts)
		if err != nil {
			return domain.BatchEmbeddingResult{}, fmt.Errorf("batch embed: %w", err)
		}
		return res, nil
	}
	res, err := domain.BatchFallback(ctx, s.embed, texts)
	if err != nil {
		return domain.BatchEmbeddingResult{}, fmt.Errorf("batch embed fallback: %w", err)
	}
	return res, nil
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
