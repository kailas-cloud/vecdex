package batch

import (
	"context"
	"errors"
	"fmt"

	"github.com/kailas-cloud/vecdex/internal/domain"
	dombatch "github.com/kailas-cloud/vecdex/internal/domain/batch"
	"github.com/kailas-cloud/vecdex/internal/domain/collection/field"
	domdoc "github.com/kailas-cloud/vecdex/internal/domain/document"
	"github.com/kailas-cloud/vecdex/internal/usecase/ingestion"
	"github.com/kailas-cloud/vecdex/internal/usecase/write"
)

// MaxBatchSize is the maximum number of items per batch request.
const MaxBatchSize = 100

// Service handles batch document operations with per-item error reporting.
type Service struct {
	embeddingSpace string
	docs           DocumentUpserter
	del            DocumentDeleter
	colls          CollectionReader
	embed          Embedder
	batchEmbed     BulkEmbedder
	maxBatchSize   int
}

// New creates a batch service. batchEmbed может быть nil — тогда fallback на поштучный Embed.
func New(
	docs DocumentUpserter, _ BulkUpserter,
	del DocumentDeleter, colls CollectionReader, embed Embedder,
	batchEmbed BulkEmbedder,
) *Service {
	return &Service{
		docs: docs,
		del:  del, colls: colls, embed: embed,
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
func (s *Service) Upsert(ctx context.Context, collectionName string, items []dombatch.UpsertItem) []dombatch.Result {
	ctx, cancel := context.WithTimeout(ctx, ingestion.BatchTimeout)
	defer cancel()
	results := make([]dombatch.Result, len(items))

	if len(items) > s.maxBatchSize {
		for i := range items {
			results[i] = dombatch.NewError(
				items[i].ID(),
				fmt.Errorf("batch size exceeds %d: %w", s.maxBatchSize, domain.ErrInvalidSchema),
			)
		}
		return results
	}

	col, err := s.colls.Get(ctx, collectionName)
	if err == nil {
		err = domain.ValidateCollectionGeneration(ctx, col.Generation())
	}
	if err != nil {
		for i := range items {
			results[i] = dombatch.NewError(items[i].ID(), fmt.Errorf("get collection: %w", err))
		}
		return results
	}

	fieldTypes := make(map[string]field.Type, len(col.Fields()))
	for _, f := range col.Fields() {
		fieldTypes[f.Name()] = f.FieldType()
	}

	ctx = domain.WithCollectionGeneration(ctx, col.Generation())
	spaceErr := col.CheckEmbeddingSpace(s.embeddingSpace)
	return s.upsertTextBatch(ctx, collectionName, items, fieldTypes, col.VectorDim(), spaceErr)
}

// upsertTextBatch validates all docs first, then batch-embeds, then bulk upserts.
func (s *Service) upsertTextBatch(
	ctx context.Context,
	collectionName string,
	items []dombatch.UpsertItem,
	fieldTypes map[string]field.Type,
	dimensions int,
	spaceErr error,
) []dombatch.Result {
	results := make([]dombatch.Result, len(items))

	prepared := s.claimBatchItems(ctx, collectionName, items, fieldTypes, results)
	defer prepared.release()
	validIdx := prepared.indices
	if len(validIdx) == 0 {
		return results
	}
	if spaceErr != nil {
		for _, idx := range validIdx {
			results[idx] = dombatch.NewError(items[idx].ID(), fmt.Errorf("batch embedding space: %w", spaceErr))
		}
		return results
	}

	// Фаза 2: batch embed
	texts := make([]string, len(validIdx))
	for j, idx := range validIdx {
		texts[j] = items[idx].Content()
	}

	embResult, err := s.doBatchEmbed(ctx, texts)
	domain.UsageFromContext(ctx).AddEmbedding(embResult.TotalTokens, embResult.UsageUnknown)

	if len(embResult.Embeddings) > len(validIdx) {
		for _, idx := range validIdx {
			results[idx] = dombatch.NewError(items[idx].ID(), domain.ErrEmbeddingProviderError)
		}
		return results
	}
	if err == nil {
		if vectorErr := validateBatchVectors(embResult.Embeddings, len(validIdx), dimensions); vectorErr != nil {
			for _, idx := range validIdx {
				results[idx] = dombatch.NewError(items[idx].ID(), vectorErr)
			}
			return results
		}
	}
	s.storePreparedItems(ctx, collectionName, items, validIdx,
		embResult.Embeddings, dimensions, results, err, prepared.contexts)

	return results
}

// doBatchEmbed использует BulkEmbedder если доступен, иначе fallback на поштучный Embed.
func (s *Service) doBatchEmbed(ctx context.Context, texts []string) (domain.BatchEmbeddingResult, error) {
	if s.batchEmbed != nil {
		res, err := s.batchEmbed.BatchEmbed(ctx, texts)
		if err != nil {
			return res, fmt.Errorf("batch embed: %w", err)
		}
		return res, nil
	}
	res, err := domain.BatchFallback(ctx, s.embed, texts)
	if err != nil {
		return res, fmt.Errorf("batch embed fallback: %w", err)
	}
	return res, nil
}

// Delete removes documents by ID in batch.
func (s *Service) Delete(ctx context.Context, collectionName string, items []dombatch.DeleteItem) []dombatch.Result {
	results := make([]dombatch.Result, len(items))

	if len(items) > s.maxBatchSize {
		for i, item := range items {
			results[i] = dombatch.NewError(
				item.ID,
				fmt.Errorf("batch size exceeds %d: %w", s.maxBatchSize, domain.ErrInvalidSchema),
			)
		}
		return results
	}

	col, err := s.colls.Get(ctx, collectionName)
	if err == nil {
		err = domain.ValidateCollectionGeneration(ctx, col.Generation())
	}
	if err != nil {
		for i, item := range items {
			results[i] = dombatch.NewError(item.ID, fmt.Errorf("get collection: %w", err))
		}
		return results
	}

	ctx = domain.WithCollectionGeneration(ctx, col.Generation())
	for i, item := range items {
		if err := validateDeleteItem(item); err != nil {
			results[i] = dombatch.NewError(item.ID, err)
			continue
		}
		itemCtx, err := batchContext(ctx, item.IfVersion, item.IdempotencyKey)
		if err != nil {
			results[i] = dombatch.NewError(item.ID, err)
			continue
		}
		claimed, replay, done, err := write.Delete(itemCtx, s.del, collectionName, item.ID)
		if err != nil {
			results[i] = dombatch.NewError(item.ID, err)
			continue
		}
		if replay != nil {
			results[i] = dombatch.NewOK(item.ID)
			continue
		}
		err = s.del.Delete(claimed, collectionName, item.ID)
		done()
		if err != nil {
			results[i] = dombatch.NewError(item.ID, fmt.Errorf("delete: %w", err))
			continue
		}
		results[i] = dombatch.NewOK(item.ID)
	}

	return results
}

func validateItemFields(doc *domdoc.Document, fieldTypes map[string]field.Type) error {
	for k := range doc.Tags() {
		if field.IsInternalName(k) {
			return fmt.Errorf("field %q is reserved: %w", k, domain.ErrInvalidSchema)
		}
		ft, ok := fieldTypes[k]
		if !ok {
			continue // stored (non-indexed) field, allowed
		}
		if ft != field.Tag {
			return fmt.Errorf("field %q is %s, not tag: %w", k, ft, domain.ErrInvalidSchema)
		}
	}
	for k := range doc.Numerics() {
		if field.IsInternalName(k) {
			return fmt.Errorf("field %q is reserved: %w", k, domain.ErrInvalidSchema)
		}
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

func validateBatchVectors(vectors [][]float32, count, dimensions int) error {
	if len(vectors) != count {
		return fmt.Errorf("embedding count mismatch: %w", domain.ErrEmbeddingProviderError)
	}
	for _, vector := range vectors {
		if err := domain.ValidateVector(vector, dimensions); err != nil {
			return fmt.Errorf("batch vector: %w", err)
		}
	}
	return nil
}

func (s *Service) storePreparedItems(
	ctx context.Context,
	collectionName string,
	items []dombatch.UpsertItem,
	validIdx []int,
	vectors [][]float32,
	dimensions int,
	results []dombatch.Result,
	embeddingErr error,
	itemContexts []context.Context,
) {
	for j, idx := range validIdx {
		doc := &items[idx].Document
		if j < len(vectors) {
			if err := domain.ValidateVector(vectors[j], dimensions); err != nil {
				results[idx] = dombatch.NewError(doc.ID(), err)
				continue
			}
			doc.SetVector(vectors[j])
			doc.SetFragments([]domdoc.Fragment{{Content: doc.Content(), Vector: doc.Vector()}})
		} else {
			if embeddingErr != nil && !errors.Is(embeddingErr, domain.ErrEmbeddingInputTooLong) {
				results[idx] = dombatch.NewError(doc.ID(), fmt.Errorf("vectorize batch item: %w", embeddingErr))
				continue
			}
			prepared, prepareErr := ingestion.Prepare(ctx, s.embed, doc.Content(), dimensions)
			domain.UsageFromContext(ctx).AddEmbedding(prepared.TotalTokens, prepared.UsageUnknown)
			if prepareErr != nil {
				results[idx] = dombatch.NewError(doc.ID(), prepareErr)
				continue
			}
			doc.SetVector(prepared.Vector)
			doc.SetFragments(prepared.Fragments)
		}
		itemCtx := itemContexts[idx]
		if _, writeErr := s.docs.Upsert(itemCtx, collectionName, doc); writeErr != nil {
			results[idx] = dombatch.NewError(doc.ID(), fmt.Errorf("upsert batch item: %w", writeErr))
			continue
		}
		results[idx] = dombatch.NewWritten(doc.ID(), doc.Version())
	}
}

func validateDeleteItem(item dombatch.DeleteItem) error {
	if err := domain.ValidateExpectedVersion(item.IfVersion); err != nil {
		return fmt.Errorf("batch delete condition: %w", err)
	}
	if _, err := domdoc.New(item.ID, "", nil, nil); err != nil {
		return fmt.Errorf("invalid batch delete ID: %w: %w", domain.ErrInvalidSchema, err)
	}
	return nil
}

func batchContext(ctx context.Context, version, key *string) (context.Context, error) {
	if err := domain.ValidateExpectedVersion(version); err != nil {
		return ctx, fmt.Errorf("batch condition: %w", err)
	}
	value := ""
	if key != nil {
		value = *key
		if err := domain.ValidateIdempotencyKey(value); err != nil {
			return ctx, fmt.Errorf("batch request key: %w", err)
		}
	}
	return domain.WithIdempotencyKey(domain.WithExpectedVersion(ctx, version), value), nil
}

// WithEmbeddingSpace binds vector operations to the configured embedding space.
func (s *Service) WithEmbeddingSpace(fingerprint string) *Service {
	s.embeddingSpace = fingerprint
	return s
}

func metadataIndexFields(types map[string]field.Type) []field.Field {
	fields := make([]field.Field, 0, len(types))
	for name, kind := range types {
		fields = append(fields, field.Reconstruct(name, kind))
	}
	return fields
}

type preparedBatch struct {
	indices  []int
	contexts []context.Context
	releases []func()
}

func (p *preparedBatch) release() {
	for _, done := range p.releases {
		done()
	}
}
func (s *Service) claimBatchItems(
	ctx context.Context, collectionName string, items []dombatch.UpsertItem,
	fieldTypes map[string]field.Type, results []dombatch.Result,
) *preparedBatch {
	prepared := &preparedBatch{contexts: make([]context.Context, len(items))}
	metadataFields := metadataIndexFields(fieldTypes)
	for i := range items {
		itemCtx, err := batchContext(ctx, items[i].IfVersion, items[i].IdempotencyKey)
		if err != nil {
			results[i] = dombatch.NewError(items[i].ID(), err)
			continue
		}
		if items[i].Content() == "" {
			results[i] = dombatch.NewError(
				items[i].ID(),
				fmt.Errorf("content is required: %w", domain.ErrInvalidSchema),
			)
			continue
		}
		if err := items[i].ProjectMetadata(metadataFields); err != nil {
			results[i] = dombatch.NewError(items[i].ID(), err)
			continue
		}
		if err := validateItemFields(&items[i].Document, fieldTypes); err != nil {
			results[i] = dombatch.NewError(items[i].ID(), err)
			continue
		}
		claimed, replay, done, err := write.Upsert(itemCtx, s.docs, collectionName, &items[i].Document)
		if err != nil {
			results[i] = dombatch.NewError(items[i].ID(), err)
			continue
		}
		prepared.releases = append(prepared.releases, done)
		if replay != nil {
			results[i] = dombatch.NewWritten(items[i].ID(), replay.Version)
			continue
		}
		prepared.contexts[i] = claimed
		prepared.indices = append(prepared.indices, i)
	}

	return prepared
}
