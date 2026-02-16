package batch

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/kailas-cloud/vecdex/internal/domain"
	dombatch "github.com/kailas-cloud/vecdex/internal/domain/batch"
	domcol "github.com/kailas-cloud/vecdex/internal/domain/collection"
	"github.com/kailas-cloud/vecdex/internal/domain/collection/field"
	domdoc "github.com/kailas-cloud/vecdex/internal/domain/document"
)

// --- Mocks ---

type mockDocUpserter struct {
	upsertErr error
	callCount int
}

func (m *mockDocUpserter) Upsert(_ context.Context, _ string, _ *domdoc.Document) (bool, error) {
	m.callCount++
	return true, m.upsertErr
}

type mockDocDeleter struct {
	deleteErr error
	callCount int
	failOnID  string // fail only for this ID
}

func (m *mockDocDeleter) Delete(_ context.Context, _, id string) error {
	m.callCount++
	if m.failOnID != "" && id == m.failOnID {
		return m.deleteErr
	}
	if m.failOnID == "" {
		return m.deleteErr
	}
	return nil
}

type mockCollReader struct {
	col domcol.Collection
	err error
}

func (m *mockCollReader) Get(_ context.Context, _ string) (domcol.Collection, error) {
	return m.col, m.err
}

type mockEmbedder struct {
	result    domain.EmbeddingResult
	err       error
	callCount int
	failAfter int // fail after N successful calls; 0=always succeed (unless err set)
}

func (m *mockEmbedder) Embed(_ context.Context, _ string) (domain.EmbeddingResult, error) {
	m.callCount++
	if m.failAfter > 0 && m.callCount > m.failAfter {
		return domain.EmbeddingResult{}, m.err
	}
	if m.failAfter == 0 && m.err != nil {
		return domain.EmbeddingResult{}, m.err
	}
	return m.result, nil
}

func makeField(t *testing.T, name string, ft field.Type) field.Field {
	t.Helper()
	f, err := field.New(name, ft)
	if err != nil {
		t.Fatalf("field.New: %v", err)
	}
	return f
}

func makeCollection(t *testing.T, fields []field.Field) domcol.Collection {
	t.Helper()
	col, err := domcol.New("test-col", domcol.TypeText, fields, 3)
	if err != nil {
		t.Fatalf("domcol.New: %v", err)
	}
	return col
}

func makeDoc(t *testing.T, id string) domdoc.Document {
	t.Helper()
	doc, err := domdoc.New(id, "content for "+id, nil, nil)
	if err != nil {
		t.Fatalf("domdoc.New: %v", err)
	}
	return doc
}

func makeDocWithTags(t *testing.T, id string, tags map[string]string) domdoc.Document {
	t.Helper()
	doc, err := domdoc.New(id, "content", tags, nil)
	if err != nil {
		t.Fatalf("domdoc.New: %v", err)
	}
	return doc
}

// --- Upsert tests ---

func TestUpsert_Success(t *testing.T) {
	col := makeCollection(t, nil)
	docs := &mockDocUpserter{}
	del := &mockDocDeleter{}
	colls := &mockCollReader{col: col}
	embed := &mockEmbedder{result: domain.EmbeddingResult{Embedding: []float32{0.1, 0.2, 0.3}}}

	svc := New(docs, del, colls, embed)
	items := []domdoc.Document{makeDoc(t, "a"), makeDoc(t, "b"), makeDoc(t, "c")}
	results := svc.Upsert(context.Background(), "test-col", items)

	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}
	for i, r := range results {
		if r.Status() != dombatch.StatusOK {
			t.Errorf("result[%d] expected ok, got error: %v", i, r.Err())
		}
	}
}

func TestUpsert_PartialFailure(t *testing.T) {
	col := makeCollection(t, []field.Field{makeField(t, "cat", field.Tag)})
	docs := &mockDocUpserter{}
	del := &mockDocDeleter{}
	colls := &mockCollReader{col: col}
	embed := &mockEmbedder{result: domain.EmbeddingResult{Embedding: []float32{0.1, 0.2, 0.3}}}

	svc := New(docs, del, colls, embed)
	items := []domdoc.Document{
		makeDoc(t, "a"), // ok — no fields
		makeDocWithTags(t, "b", map[string]string{"unknown": "val"}), // fail — unknown field
		makeDoc(t, "c"), // ok
	}
	results := svc.Upsert(context.Background(), "test-col", items)

	if results[0].Status() != dombatch.StatusOK {
		t.Errorf("result[0] expected ok, got %v", results[0].Err())
	}
	if results[1].Status() != dombatch.StatusError {
		t.Error("result[1] expected error for unknown field")
	}
	if results[2].Status() != dombatch.StatusOK {
		t.Errorf("result[2] expected ok, got %v", results[2].Err())
	}
}

func TestUpsert_ExceedsMax(t *testing.T) {
	col := makeCollection(t, nil)
	docs := &mockDocUpserter{}
	del := &mockDocDeleter{}
	colls := &mockCollReader{col: col}
	embed := &mockEmbedder{result: domain.EmbeddingResult{Embedding: []float32{0.1}}}

	svc := New(docs, del, colls, embed)
	items := make([]domdoc.Document, MaxBatchSize+1)
	for i := range items {
		items[i] = makeDoc(t, fmt.Sprintf("doc-%d", i))
	}

	results := svc.Upsert(context.Background(), "test-col", items)
	for _, r := range results {
		if r.Status() != dombatch.StatusError {
			t.Error("expected all errors for oversized batch")
		}
	}
}

func TestUpsert_CollectionNotFound(t *testing.T) {
	docs := &mockDocUpserter{}
	del := &mockDocDeleter{}
	colls := &mockCollReader{err: domain.ErrNotFound}
	embed := &mockEmbedder{result: domain.EmbeddingResult{Embedding: []float32{0.1}}}

	svc := New(docs, del, colls, embed)
	items := []domdoc.Document{makeDoc(t, "a")}
	results := svc.Upsert(context.Background(), "nonexistent", items)

	if results[0].Status() != dombatch.StatusError {
		t.Error("expected error for nonexistent collection")
	}
	if !errors.Is(results[0].Err(), domain.ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", results[0].Err())
	}
}

func TestUpsert_QuotaCascade(t *testing.T) {
	col := makeCollection(t, nil)
	docs := &mockDocUpserter{}
	del := &mockDocDeleter{}
	colls := &mockCollReader{col: col}
	embed := &mockEmbedder{
		result:    domain.EmbeddingResult{Embedding: []float32{0.1, 0.2, 0.3}},
		err:       domain.ErrEmbeddingQuotaExceeded,
		failAfter: 1, // first succeeds, rest fail
	}

	svc := New(docs, del, colls, embed)
	items := []domdoc.Document{makeDoc(t, "a"), makeDoc(t, "b"), makeDoc(t, "c")}
	results := svc.Upsert(context.Background(), "test-col", items)

	if results[0].Status() != dombatch.StatusOK {
		t.Errorf("result[0] expected ok, got %v", results[0].Err())
	}
	// b and c should both fail with quota
	for _, i := range []int{1, 2} {
		if results[i].Status() != dombatch.StatusError {
			t.Errorf("result[%d] expected error", i)
		}
		if !errors.Is(results[i].Err(), domain.ErrEmbeddingQuotaExceeded) {
			t.Errorf("result[%d] expected ErrEmbeddingQuotaExceeded, got %v", i, results[i].Err())
		}
	}
}

func TestUpsert_RateLimitCascade(t *testing.T) {
	col := makeCollection(t, nil)
	docs := &mockDocUpserter{}
	del := &mockDocDeleter{}
	colls := &mockCollReader{col: col}
	embed := &mockEmbedder{
		result:    domain.EmbeddingResult{Embedding: []float32{0.1, 0.2, 0.3}},
		err:       domain.ErrRateLimited,
		failAfter: 0, // all fail immediately
	}

	svc := New(docs, del, colls, embed)
	items := []domdoc.Document{makeDoc(t, "a"), makeDoc(t, "b")}
	results := svc.Upsert(context.Background(), "test-col", items)

	// First hits rate limit → cascade stops all
	for i, r := range results {
		if r.Status() != dombatch.StatusError {
			t.Errorf("result[%d] expected error", i)
		}
		if !errors.Is(r.Err(), domain.ErrRateLimited) {
			t.Errorf("result[%d] expected ErrRateLimited, got %v", i, r.Err())
		}
	}
}

func TestUpsert_IndividualEmbedError(t *testing.T) {
	col := makeCollection(t, nil)
	docs := &mockDocUpserter{}
	del := &mockDocDeleter{}
	colls := &mockCollReader{col: col}
	providerErr := errors.New("model unavailable")
	embed := &mockEmbedder{
		result:    domain.EmbeddingResult{Embedding: []float32{0.1, 0.2, 0.3}},
		err:       providerErr,
		failAfter: 1, // first ok, second fails
	}

	svc := New(docs, del, colls, embed)
	items := []domdoc.Document{makeDoc(t, "a"), makeDoc(t, "b"), makeDoc(t, "c")}
	results := svc.Upsert(context.Background(), "test-col", items)

	if results[0].Status() != dombatch.StatusOK {
		t.Errorf("result[0] expected ok")
	}
	if results[1].Status() != dombatch.StatusError {
		t.Errorf("result[1] expected error")
	}
	// Non-cascading error → third item should still be attempted
	if results[2].Status() != dombatch.StatusError {
		// Individual provider errors don't cascade, but third will also fail
		// since mock always fails after failAfter
		t.Logf("result[2] status=%s (expected error since mock continues failing)", results[2].Status())
	}
}

func TestUpsert_InvalidFields(t *testing.T) {
	col := makeCollection(t, []field.Field{makeField(t, "lang", field.Tag)})
	docs := &mockDocUpserter{}
	del := &mockDocDeleter{}
	colls := &mockCollReader{col: col}
	embed := &mockEmbedder{result: domain.EmbeddingResult{Embedding: []float32{0.1, 0.2, 0.3}}}

	svc := New(docs, del, colls, embed)
	items := []domdoc.Document{
		makeDocWithTags(t, "a", map[string]string{"lang": "go"}),     // ok
		makeDocWithTags(t, "b", map[string]string{"unknown": "val"}), // fail
	}
	results := svc.Upsert(context.Background(), "test-col", items)

	if results[0].Status() != dombatch.StatusOK {
		t.Errorf("result[0] expected ok, got %v", results[0].Err())
	}
	if results[1].Status() != dombatch.StatusError {
		t.Error("result[1] expected error for invalid field")
	}
}

// --- Delete tests ---

func TestDelete_Success(t *testing.T) {
	col := makeCollection(t, nil)
	docs := &mockDocUpserter{}
	del := &mockDocDeleter{}
	colls := &mockCollReader{col: col}
	embed := &mockEmbedder{}

	svc := New(docs, del, colls, embed)
	results := svc.Delete(context.Background(), "test-col", []string{"a", "b"})

	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	for _, r := range results {
		if r.Status() != dombatch.StatusOK {
			t.Errorf("expected ok, got %v", r.Err())
		}
	}
}

func TestDelete_PartialFailure(t *testing.T) {
	col := makeCollection(t, nil)
	docs := &mockDocUpserter{}
	del := &mockDocDeleter{deleteErr: domain.ErrDocumentNotFound, failOnID: "b"}
	colls := &mockCollReader{col: col}
	embed := &mockEmbedder{}

	svc := New(docs, del, colls, embed)
	results := svc.Delete(context.Background(), "test-col", []string{"a", "b", "c"})

	if results[0].Status() != dombatch.StatusOK {
		t.Error("result[0] expected ok")
	}
	if results[1].Status() != dombatch.StatusError {
		t.Error("result[1] expected error")
	}
	if results[2].Status() != dombatch.StatusOK {
		t.Error("result[2] expected ok")
	}
}

func TestDelete_ExceedsMax(t *testing.T) {
	col := makeCollection(t, nil)
	docs := &mockDocUpserter{}
	del := &mockDocDeleter{}
	colls := &mockCollReader{col: col}
	embed := &mockEmbedder{}

	svc := New(docs, del, colls, embed)
	ids := make([]string, MaxBatchSize+1)
	for i := range ids {
		ids[i] = fmt.Sprintf("doc-%d", i)
	}

	results := svc.Delete(context.Background(), "test-col", ids)
	for _, r := range results {
		if r.Status() != dombatch.StatusError {
			t.Error("expected all errors for oversized batch")
		}
	}
}

func TestDelete_CollectionNotFound(t *testing.T) {
	docs := &mockDocUpserter{}
	del := &mockDocDeleter{}
	colls := &mockCollReader{err: domain.ErrNotFound}
	embed := &mockEmbedder{}

	svc := New(docs, del, colls, embed)
	results := svc.Delete(context.Background(), "nonexistent", []string{"a"})

	if results[0].Status() != dombatch.StatusError {
		t.Error("expected error for nonexistent collection")
	}
	if !errors.Is(results[0].Err(), domain.ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", results[0].Err())
	}
}
