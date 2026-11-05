package vecdex

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/kailas-cloud/vecdex/internal/domain"
	dombatch "github.com/kailas-cloud/vecdex/internal/domain/batch"
	domdoc "github.com/kailas-cloud/vecdex/internal/domain/document"
	"github.com/kailas-cloud/vecdex/internal/domain/document/patch"
	"github.com/kailas-cloud/vecdex/internal/domain/metadata"
)

// DocumentService manages documents within a single collection.
type DocumentService struct {
	collection string
	docSvc     documentUseCase
	batchSvc   batchUseCase
	obs        *observer
}

// Upsert creates or updates a document. Returns the committed source version.
func (s *DocumentService) Upsert(
	ctx context.Context, doc Document, conditions ...WriteOption,
) (_ WriteResult, err error) {
	start := time.Now()
	defer func() { s.obs.observe("document.upsert", start, err) }()

	ctx, err = applyWriteOptions(ctx, conditions)
	if err != nil {
		return WriteResult{}, err
	}
	d, err := toInternalDocument(doc)
	if err != nil {
		return WriteResult{}, fmt.Errorf("upsert: %w", err)
	}
	created, err := s.docSvc.Upsert(ctx, s.collection, &d)
	if err != nil {
		return WriteResult{}, fmt.Errorf("upsert: %w", err)
	}
	return WriteResult{ID: d.ID(), Version: d.Version(), Created: created}, nil
}

// Get retrieves a document by ID.
func (s *DocumentService) Get(
	ctx context.Context, id string,
) (_ Document, err error) {
	start := time.Now()
	defer func() { s.obs.observe("document.get", start, err) }()

	d, err := s.docSvc.Get(ctx, s.collection, id)
	if err != nil {
		return Document{}, fmt.Errorf("get document: %w", err)
	}
	return fromInternalDocument(&d), nil
}

// List returns a paginated list of documents.
func (s *DocumentService) List(
	ctx context.Context, cursor string, limit int,
) (_ ListResult, err error) {
	start := time.Now()
	defer func() { s.obs.observe("document.list", start, err) }()

	docs, next, err := s.docSvc.List(ctx, s.collection, cursor, limit)
	if err != nil {
		return ListResult{}, fmt.Errorf("list documents: %w", err)
	}
	out := make([]Document, len(docs))
	for i, d := range docs {
		out[i] = fromInternalDocument(&d)
	}
	return ListResult{Documents: out, NextCursor: next}, nil
}

// Delete removes a document by ID.
func (s *DocumentService) Delete(
	ctx context.Context, id string, conditions ...WriteOption,
) (err error) {
	start := time.Now()
	defer func() { s.obs.observe("document.delete", start, err) }()

	ctx, err = applyWriteOptions(ctx, conditions)
	if err != nil {
		return err
	}
	if err = s.docSvc.Delete(ctx, s.collection, id); err != nil {
		return fmt.Errorf("delete document: %w", err)
	}
	return nil
}

// Patch applies a partial update to a document.
func (s *DocumentService) Patch(
	ctx context.Context, id string, p DocumentPatch, conditions ...WriteOption,
) (_ WriteResult, err error) {
	start := time.Now()
	defer func() { s.obs.observe("document.patch", start, err) }()

	ctx, err = applyWriteOptions(ctx, conditions)
	if err != nil {
		return WriteResult{}, err
	}
	dp, err := toInternalPatch(p)
	if err != nil {
		return WriteResult{}, fmt.Errorf("patch: %w", err)
	}
	d, err := s.docSvc.Patch(ctx, s.collection, id, dp)
	if err != nil {
		return WriteResult{}, fmt.Errorf("patch: %w", err)
	}
	return WriteResult{ID: d.ID, Version: d.Version, Created: d.Created}, nil
}

// Count returns the number of documents in the collection.
func (s *DocumentService) Count(
	ctx context.Context,
) (_ int, err error) {
	start := time.Now()
	defer func() { s.obs.observe("document.count", start, err) }()

	n, err := s.docSvc.Count(ctx, s.collection)
	if err != nil {
		return 0, fmt.Errorf("count: %w", err)
	}
	return n, nil
}

// BatchUpsert creates or updates documents in batch.
func (s *DocumentService) BatchUpsert(
	ctx context.Context, docs []BatchUpsertItem,
) (_ BatchResponse, err error) {
	start := time.Now()
	defer func() { s.obs.observe("document.batch_upsert", start, err) }()
	if len(docs) == 0 {
		return BatchResponse{}, nil
	}

	items := make([]dombatch.UpsertItem, len(docs))
	for i, d := range docs {
		items[i].Document, err = toInternalDocument(Document{
			ID: d.ID, Content: d.Content, Metadata: d.Metadata, ExpiresAt: d.ExpiresAt,
		})
		if err != nil {
			return BatchResponse{}, fmt.Errorf("document %d: %w", i, err)
		}
		if d.IfVersion != "" {
			version := d.IfVersion
			items[i].IfVersion = &version
		}
		if d.IdempotencyKey != "" {
			key := d.IdempotencyKey
			items[i].IdempotencyKey = &key
		}
	}
	results := s.batchSvc.Upsert(ctx, s.collection, items)
	return toBatchResponse(results), nil
}

// BatchDelete removes documents by IDs.
func (s *DocumentService) BatchDelete(
	ctx context.Context, items []BatchDeleteItem,
) BatchResponse {
	start := time.Now()
	defer func() { s.obs.observe("document.batch_delete", start, nil) }()
	if len(items) == 0 {
		return BatchResponse{}
	}

	commands := make([]dombatch.DeleteItem, len(items))
	for i, item := range items {
		commands[i].ID = item.ID
		if item.IfVersion != "" {
			version := item.IfVersion
			commands[i].IfVersion = &version
		}
		if item.IdempotencyKey != "" {
			key := item.IdempotencyKey
			commands[i].IdempotencyKey = &key
		}
	}
	results := s.batchSvc.Delete(ctx, s.collection, commands)
	return toBatchResponse(results)
}

func toInternalDocument(d Document) (domdoc.Document, error) {
	doc, err := domdoc.New(d.ID, d.Content, nil, nil)
	if err != nil {
		return domdoc.Document{}, fmt.Errorf("validate document: %w", err)
	}
	values := d.Metadata
	if values == nil {
		values = map[string]any{}
	}
	raw, err := json.Marshal(values)
	if err != nil {
		return domdoc.Document{}, fmt.Errorf("encode metadata: %w: %w", domain.ErrInvalidSchema, err)
	}
	value, err := metadata.Parse(raw)
	if err != nil {
		return domdoc.Document{}, fmt.Errorf("decode document metadata: %w", err)
	}
	doc.SetMetadata(value)
	if d.ExpiresAt != nil {
		if *d.ExpiresAt <= 0 {
			return domdoc.Document{}, fmt.Errorf("expires_at must be positive: %w", domain.ErrInvalidSchema)
		}
		if err := doc.SetExpiresAt(*d.ExpiresAt); err != nil {
			return domdoc.Document{}, fmt.Errorf("validate source expiration: %w", err)
		}
	}
	return doc, nil
}

func fromInternalDocument(d *domdoc.Document) Document {
	return Document{
		ExpiresAt: optionalExpiresAt(d.ExpiresAt()),
		Metadata:  sourceMetadataValues(d.Metadata(), d.Tags(), d.Numerics()),
		ID:        d.ID(), Content: d.Content(), Version: d.Version(),
	}
}

func sourceMetadataValues(value metadata.Object, tags map[string]string, numerics map[string]float64) map[string]any {
	if value.Present() {
		return value.Values()
	}
	values := make(map[string]any, len(tags)+len(numerics))
	for key, item := range tags {
		values[key] = item
	}
	for key, item := range numerics {
		values[key] = item
	}
	return values
}

func toInternalPatch(p DocumentPatch) (patch.Patch, error) {
	var raw json.RawMessage
	if p.Metadata != nil {
		encoded, err := json.Marshal(*p.Metadata)
		if err != nil {
			return patch.Patch{}, fmt.Errorf("encode metadata patch: %w: %w", domain.ErrInvalidSchema, err)
		}
		raw = encoded
	}
	value, err := patch.NewSource(p.Content, raw, p.ExpiresAt)
	if err != nil {
		return patch.Patch{}, fmt.Errorf("validate metadata patch: %w", err)
	}
	return value, nil
}

func optionalExpiresAt(value int64) *int64 {
	if value == 0 {
		return nil
	}
	return &value
}

func toBatchResponse(results []dombatch.Result) BatchResponse {
	out := make([]BatchResult, len(results))
	succeeded, failed := 0, 0
	for i, r := range results {
		ok := r.Status() == dombatch.StatusOK
		out[i] = BatchResult{
			Version: r.Version(),
			ID:      r.ID(),
			OK:      ok,
			Err:     r.Err(),
		}
		if ok {
			succeeded++
		} else {
			failed++
		}
	}
	return BatchResponse{Results: out, Succeeded: succeeded, Failed: failed}
}
