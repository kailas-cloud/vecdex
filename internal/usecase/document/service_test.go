package document

import (
	"context"
	"errors"
	"testing"

	"github.com/kailas-cloud/vecdex/internal/domain"
	domcol "github.com/kailas-cloud/vecdex/internal/domain/collection"
	"github.com/kailas-cloud/vecdex/internal/domain/collection/field"
	domdoc "github.com/kailas-cloud/vecdex/internal/domain/document"
	"github.com/kailas-cloud/vecdex/internal/domain/document/patch"
)

// --- Mocks ---

type mockDocRepo struct {
	upsertCreated bool
	upsertErr     error
	getResult     domdoc.Document
	getErr        error
	listDocs      []domdoc.Document
	listCursor    string
	listErr       error
	deleteErr     error
	patchErr      error
	countResult   int
	countErr      error
}

func (m *mockDocRepo) Upsert(_ context.Context, _ string, _ *domdoc.Document) (bool, error) {
	return m.upsertCreated, m.upsertErr
}
func (m *mockDocRepo) Get(_ context.Context, _, _ string) (domdoc.Document, error) {
	return m.getResult, m.getErr
}
func (m *mockDocRepo) List(_ context.Context, _, _ string, _ int) ([]domdoc.Document, string, error) {
	return m.listDocs, m.listCursor, m.listErr
}
func (m *mockDocRepo) Delete(_ context.Context, _, _ string) error {
	return m.deleteErr
}
func (m *mockDocRepo) Patch(_ context.Context, _, _ string, _ patch.Patch, _ []float32) error {
	return m.patchErr
}
func (m *mockDocRepo) Count(_ context.Context, _ string) (int, error) {
	return m.countResult, m.countErr
}

type mockCollReader struct {
	col domcol.Collection
	err error
}

func (m *mockCollReader) Get(_ context.Context, _ string) (domcol.Collection, error) {
	return m.col, m.err
}

type mockEmbedder struct {
	result domain.EmbeddingResult
	err    error
}

func (m *mockEmbedder) Embed(_ context.Context, _ string) (domain.EmbeddingResult, error) {
	if m.err != nil {
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

func makeDoc(t *testing.T, id, content string) domdoc.Document {
	t.Helper()
	doc, err := domdoc.New(id, content, nil, nil)
	if err != nil {
		t.Fatalf("domdoc.New: %v", err)
	}
	return doc
}

func makeDocWithTags(t *testing.T, id, content string, tags map[string]string) domdoc.Document {
	t.Helper()
	doc, err := domdoc.New(id, content, tags, nil)
	if err != nil {
		t.Fatalf("domdoc.New: %v", err)
	}
	return doc
}

// --- Upsert tests ---

func TestUpsert_CreateSuccess(t *testing.T) {
	col := makeCollection(t, nil)
	repo := &mockDocRepo{upsertCreated: true}
	colls := &mockCollReader{col: col}
	embed := &mockEmbedder{result: domain.EmbeddingResult{Embedding: []float32{0.1, 0.2, 0.3}}}

	svc := New(repo, colls, embed, embed)
	doc := makeDoc(t, "doc-1", "hello world")

	created, err := svc.Upsert(context.Background(), "test-col", &doc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !created {
		t.Error("expected created=true")
	}
}

func TestUpsert_UpdateSuccess(t *testing.T) {
	col := makeCollection(t, nil)
	repo := &mockDocRepo{upsertCreated: false}
	colls := &mockCollReader{col: col}
	embed := &mockEmbedder{result: domain.EmbeddingResult{Embedding: []float32{0.1, 0.2, 0.3}}}

	svc := New(repo, colls, embed, embed)
	doc := makeDoc(t, "doc-1", "updated content")

	created, err := svc.Upsert(context.Background(), "test-col", &doc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if created {
		t.Error("expected created=false for update")
	}
}

func TestUpsert_CollectionNotFound(t *testing.T) {
	repo := &mockDocRepo{}
	colls := &mockCollReader{err: domain.ErrNotFound}
	embed := &mockEmbedder{result: domain.EmbeddingResult{Embedding: []float32{0.1}}}

	svc := New(repo, colls, embed, embed)
	doc := makeDoc(t, "doc-1", "content")

	_, err := svc.Upsert(context.Background(), "nonexistent", &doc)
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestUpsert_UnknownTagAllowed(t *testing.T) {
	col := makeCollection(t, []field.Field{makeField(t, "category", field.Tag)})
	repo := &mockDocRepo{}
	colls := &mockCollReader{col: col}
	embed := &mockEmbedder{result: domain.EmbeddingResult{Embedding: []float32{0.1, 0.2, 0.3}}}

	svc := New(repo, colls, embed, embed)
	// Document with tag "unknown" not in schema — stored but not indexed
	doc := makeDocWithTags(t, "doc-1", "content", map[string]string{"unknown": "val"})

	_, err := svc.Upsert(context.Background(), "test-col", &doc)
	if err != nil {
		t.Fatalf("unexpected error: unknown tags should be allowed (stored fields): %v", err)
	}
}

func TestUpsert_TypeMismatch(t *testing.T) {
	col := makeCollection(t, []field.Field{makeField(t, "rating", field.Numeric)})
	repo := &mockDocRepo{}
	colls := &mockCollReader{col: col}
	embed := &mockEmbedder{result: domain.EmbeddingResult{Embedding: []float32{0.1, 0.2, 0.3}}}

	svc := New(repo, colls, embed, embed)
	// "rating" is defined as numeric but passed as tag
	doc := makeDocWithTags(t, "doc-1", "content", map[string]string{"rating": "5"})

	_, err := svc.Upsert(context.Background(), "test-col", &doc)
	if err == nil {
		t.Fatal("expected error for type mismatch")
	}
	if !errors.Is(err, domain.ErrInvalidSchema) {
		t.Errorf("expected ErrInvalidSchema, got %v", err)
	}
}

func TestUpsert_EmbedError(t *testing.T) {
	col := makeCollection(t, nil)
	repo := &mockDocRepo{}
	colls := &mockCollReader{col: col}
	embedErr := errors.New("provider timeout")
	embed := &mockEmbedder{err: embedErr}

	svc := New(repo, colls, embed, embed)
	doc := makeDoc(t, "doc-1", "content")

	_, err := svc.Upsert(context.Background(), "test-col", &doc)
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, embedErr) {
		t.Errorf("expected embed error wrapped, got %v", err)
	}
}

func TestUpsert_DimMismatch(t *testing.T) {
	col := makeCollection(t, nil) // vectorDim=3
	repo := &mockDocRepo{}
	colls := &mockCollReader{col: col}
	embed := &mockEmbedder{result: domain.EmbeddingResult{Embedding: []float32{0.1, 0.2}}} // 2 dims, expects 3

	svc := New(repo, colls, embed, embed)
	doc := makeDoc(t, "doc-1", "content")

	_, err := svc.Upsert(context.Background(), "test-col", &doc)
	if err == nil {
		t.Fatal("expected error for dim mismatch")
	}
	if !errors.Is(err, domain.ErrVectorDimMismatch) {
		t.Errorf("expected ErrVectorDimMismatch, got %v", err)
	}
}

// --- Get tests ---

func TestGet_Success(t *testing.T) {
	expected := makeDoc(t, "doc-1", "hello")
	col := makeCollection(t, nil)
	repo := &mockDocRepo{getResult: expected}
	colls := &mockCollReader{col: col}
	embed := &mockEmbedder{}

	svc := New(repo, colls, embed, embed)
	doc, err := svc.Get(context.Background(), "test-col", "doc-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if doc.ID() != "doc-1" {
		t.Errorf("expected ID 'doc-1', got %q", doc.ID())
	}
}

func TestGet_CollectionNotFound(t *testing.T) {
	repo := &mockDocRepo{}
	colls := &mockCollReader{err: domain.ErrNotFound}
	embed := &mockEmbedder{}

	svc := New(repo, colls, embed, embed)
	_, err := svc.Get(context.Background(), "nonexistent", "doc-1")
	if !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestGet_DocumentNotFound(t *testing.T) {
	col := makeCollection(t, nil)
	repo := &mockDocRepo{getErr: domain.ErrDocumentNotFound}
	colls := &mockCollReader{col: col}
	embed := &mockEmbedder{}

	svc := New(repo, colls, embed, embed)
	_, err := svc.Get(context.Background(), "test-col", "nonexistent")
	if !errors.Is(err, domain.ErrDocumentNotFound) {
		t.Errorf("expected ErrDocumentNotFound, got %v", err)
	}
}

// --- Delete tests ---

func TestDelete_Success(t *testing.T) {
	col := makeCollection(t, nil)
	repo := &mockDocRepo{}
	colls := &mockCollReader{col: col}
	embed := &mockEmbedder{}

	svc := New(repo, colls, embed, embed)
	if err := svc.Delete(context.Background(), "test-col", "doc-1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestDelete_CollectionNotFound(t *testing.T) {
	repo := &mockDocRepo{}
	colls := &mockCollReader{err: domain.ErrNotFound}
	embed := &mockEmbedder{}

	svc := New(repo, colls, embed, embed)
	err := svc.Delete(context.Background(), "nonexistent", "doc-1")
	if !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestDelete_DocumentNotFound(t *testing.T) {
	col := makeCollection(t, nil)
	repo := &mockDocRepo{deleteErr: domain.ErrDocumentNotFound}
	colls := &mockCollReader{col: col}
	embed := &mockEmbedder{}

	svc := New(repo, colls, embed, embed)
	err := svc.Delete(context.Background(), "test-col", "nonexistent")
	if !errors.Is(err, domain.ErrDocumentNotFound) {
		t.Errorf("expected ErrDocumentNotFound, got %v", err)
	}
}

// --- List tests ---

func TestList_DefaultLimit(t *testing.T) {
	col := makeCollection(t, nil)
	docs := []domdoc.Document{makeDoc(t, "a", "text")}
	repo := &mockDocRepo{listDocs: docs}
	colls := &mockCollReader{col: col}
	embed := &mockEmbedder{}

	svc := New(repo, colls, embed, embed)
	result, cursor, err := svc.List(context.Background(), "test-col", "", 0) // 0 → default 20
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 1 {
		t.Errorf("expected 1 doc, got %d", len(result))
	}
	if cursor != "" {
		t.Errorf("expected empty cursor, got %q", cursor)
	}
}

func TestList_MaxLimit(t *testing.T) {
	col := makeCollection(t, nil)
	repo := &mockDocRepo{listDocs: []domdoc.Document{}}
	colls := &mockCollReader{col: col}
	embed := &mockEmbedder{}

	svc := New(repo, colls, embed, embed)
	// Limit 999 should be capped to 100
	_, _, err := svc.List(context.Background(), "test-col", "", 999)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestList_WithCursor(t *testing.T) {
	col := makeCollection(t, nil)
	docs := []domdoc.Document{makeDoc(t, "b", "text")}
	repo := &mockDocRepo{listDocs: docs, listCursor: "next-page"}
	colls := &mockCollReader{col: col}
	embed := &mockEmbedder{}

	svc := New(repo, colls, embed, embed)
	result, cursor, err := svc.List(context.Background(), "test-col", "prev-cursor", 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 1 {
		t.Errorf("expected 1 doc, got %d", len(result))
	}
	if cursor != "next-page" {
		t.Errorf("expected cursor 'next-page', got %q", cursor)
	}
}

// --- Patch tests ---

func TestPatch_MetadataOnly(t *testing.T) {
	col := makeCollection(t, []field.Field{makeField(t, "lang", field.Tag)})
	updated := makeDocWithTags(t, "doc-1", "content", map[string]string{"lang": "go"})
	repo := &mockDocRepo{getResult: updated}
	colls := &mockCollReader{col: col}
	embed := &mockEmbedder{}

	svc := New(repo, colls, embed, embed)
	val := "go"
	p, _ := patch.New(nil, map[string]*string{"lang": &val}, nil)

	doc, err := svc.Patch(context.Background(), "test-col", "doc-1", p)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if doc.ID() != "doc-1" {
		t.Errorf("expected ID 'doc-1', got %q", doc.ID())
	}
}

func TestPatch_WithContent(t *testing.T) {
	col := makeCollection(t, nil)
	updated := makeDoc(t, "doc-1", "new content")
	repo := &mockDocRepo{getResult: updated}
	colls := &mockCollReader{col: col}
	embed := &mockEmbedder{result: domain.EmbeddingResult{Embedding: []float32{0.1, 0.2, 0.3}}}

	svc := New(repo, colls, embed, embed)
	content := "new content"
	p, _ := patch.New(&content, nil, nil)

	doc, err := svc.Patch(context.Background(), "test-col", "doc-1", p)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if doc.Content() != "new content" {
		t.Errorf("expected 'new content', got %q", doc.Content())
	}
}

func TestPatch_NotFound(t *testing.T) {
	col := makeCollection(t, nil)
	repo := &mockDocRepo{patchErr: domain.ErrDocumentNotFound}
	colls := &mockCollReader{col: col}
	embed := &mockEmbedder{result: domain.EmbeddingResult{Embedding: []float32{0.1, 0.2, 0.3}}}

	svc := New(repo, colls, embed, embed)
	content := "new content"
	p, _ := patch.New(&content, nil, nil)

	_, err := svc.Patch(context.Background(), "test-col", "nonexistent", p)
	if !errors.Is(err, domain.ErrDocumentNotFound) {
		t.Errorf("expected ErrDocumentNotFound, got %v", err)
	}
}

// --- Count tests ---

func TestCount_Success(t *testing.T) {
	col := makeCollection(t, nil)
	repo := &mockDocRepo{countResult: 42}
	colls := &mockCollReader{col: col}
	embed := &mockEmbedder{}

	svc := New(repo, colls, embed, embed)
	count, err := svc.Count(context.Background(), "test-col")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if count != 42 {
		t.Errorf("expected 42, got %d", count)
	}
}

func TestCount_CollectionNotFound(t *testing.T) {
	repo := &mockDocRepo{}
	colls := &mockCollReader{err: domain.ErrNotFound}
	embed := &mockEmbedder{}

	svc := New(repo, colls, embed, embed)
	_, err := svc.Count(context.Background(), "nonexistent")
	if !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}
