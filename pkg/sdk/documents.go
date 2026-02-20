package vecdex

import (
	"context"
	"fmt"
	"time"

	dombatch "github.com/kailas-cloud/vecdex/internal/domain/batch"
	domdoc "github.com/kailas-cloud/vecdex/internal/domain/document"
	"github.com/kailas-cloud/vecdex/internal/domain/document/patch"
)

// DocumentService manages documents within a single collection.
type DocumentService struct {
	collection string
	docSvc     documentUseCase
	batchSvc   batchUseCase
	obs        *observer
}

// Upsert creates or updates a document. Returns true if created.
func (s *DocumentService) Upsert(
	ctx context.Context, doc Document,
) (_ bool, err error) {
	start := time.Now()
	defer func() { s.obs.observe("document.upsert", start, err) }()

	d, err := toInternalDocument(doc)
	if err != nil {
		return false, fmt.Errorf("upsert: %w", err)
	}
	created, err := s.docSvc.Upsert(ctx, s.collection, &d)
	if err != nil {
		return false, fmt.Errorf("upsert: %w", err)
	}
	return created, nil
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
	return fromInternalDocument(d), nil
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
		out[i] = fromInternalDocument(d)
	}
	return ListResult{Documents: out, NextCursor: next}, nil
}

// Delete removes a document by ID.
func (s *DocumentService) Delete(
	ctx context.Context, id string,
) (err error) {
	start := time.Now()
	defer func() { s.obs.observe("document.delete", start, err) }()

	if err = s.docSvc.Delete(ctx, s.collection, id); err != nil {
		return fmt.Errorf("delete document: %w", err)
	}
	return nil
}

// Patch applies a partial update to a document.
func (s *DocumentService) Patch(
	ctx context.Context, id string, p DocumentPatch,
) (_ Document, err error) {
	start := time.Now()
	defer func() { s.obs.observe("document.patch", start, err) }()

	dp, err := toInternalPatch(p)
	if err != nil {
		return Document{}, fmt.Errorf("patch: %w", err)
	}
	d, err := s.docSvc.Patch(ctx, s.collection, id, dp)
	if err != nil {
		return Document{}, fmt.Errorf("patch: %w", err)
	}
	return fromInternalDocument(d), nil
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
	ctx context.Context, docs []Document,
) (_ BatchResponse, err error) {
	start := time.Now()
	defer func() { s.obs.observe("document.batch_upsert", start, err) }()

	items := make([]domdoc.Document, len(docs))
	for i, d := range docs {
		items[i], err = toInternalDocument(d)
		if err != nil {
			return BatchResponse{}, fmt.Errorf("document %d: %w", i, err)
		}
	}
	results := s.batchSvc.Upsert(ctx, s.collection, items)
	return toBatchResponse(results), nil
}

// BatchDelete removes documents by IDs.
func (s *DocumentService) BatchDelete(
	ctx context.Context, ids []string,
) BatchResponse {
	start := time.Now()
	defer func() { s.obs.observe("document.batch_delete", start, nil) }()

	results := s.batchSvc.Delete(ctx, s.collection, ids)
	return toBatchResponse(results)
}

func toInternalDocument(d Document) (domdoc.Document, error) {
	doc, err := domdoc.New(d.ID, d.Content, d.Tags, d.Numerics)
	if err != nil {
		return domdoc.Document{}, fmt.Errorf("validate document: %w", err)
	}
	return doc, nil
}

func fromInternalDocument(d domdoc.Document) Document {
	return Document{
		ID:       d.ID(),
		Content:  d.Content(),
		Revision: d.Revision(),
		Tags:     cloneTags(d.Tags()),
		Numerics: cloneNumerics(d.Numerics()),
	}
}

func cloneTags(m map[string]string) map[string]string {
	if m == nil {
		return nil
	}
	c := make(map[string]string, len(m))
	for k, v := range m {
		c[k] = v
	}
	return c
}

func cloneNumerics(m map[string]float64) map[string]float64 {
	if m == nil {
		return nil
	}
	c := make(map[string]float64, len(m))
	for k, v := range m {
		c[k] = v
	}
	return c
}

func toInternalPatch(p DocumentPatch) (patch.Patch, error) {
	pp, err := patch.New(p.Content, p.Tags, p.Numerics)
	if err != nil {
		return patch.Patch{}, fmt.Errorf("validate patch: %w", err)
	}
	return pp, nil
}

func toBatchResponse(results []dombatch.Result) BatchResponse {
	out := make([]BatchResult, len(results))
	succeeded, failed := 0, 0
	for i, r := range results {
		ok := r.Status() == dombatch.StatusOK
		out[i] = BatchResult{
			ID:  r.ID(),
			OK:  ok,
			Err: r.Err(),
		}
		if ok {
			succeeded++
		} else {
			failed++
		}
	}
	return BatchResponse{Results: out, Succeeded: succeeded, Failed: failed}
}
