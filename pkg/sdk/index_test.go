package vecdex

import (
	"context"
	"errors"
	"testing"

	dombatch "github.com/kailas-cloud/vecdex/internal/domain/batch"
	domcol "github.com/kailas-cloud/vecdex/internal/domain/collection"
	"github.com/kailas-cloud/vecdex/internal/domain/collection/field"
	domdoc "github.com/kailas-cloud/vecdex/internal/domain/document"
	"github.com/kailas-cloud/vecdex/internal/domain/document/patch"
	"github.com/kailas-cloud/vecdex/internal/domain/search/request"
	"github.com/kailas-cloud/vecdex/internal/domain/search/result"
)

func TestNewIndex_ValidText(t *testing.T) {
	idx, err := NewIndex[textDoc](nil, "test-docs")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if idx.name != "test-docs" {
		t.Errorf("name = %q, want test-docs", idx.name)
	}
	if idx.meta.colType != CollectionTypeText {
		t.Errorf("colType = %q, want text", idx.meta.colType)
	}
}

func TestNewIndex_InvalidStruct(t *testing.T) {
	_, err := NewIndex[noIDDoc](nil, "bad")
	if err == nil {
		t.Fatal("expected error for struct without id tag")
	}
}

func TestNewIndex_NonStruct(t *testing.T) {
	_, err := NewIndex[int](nil, "bad")
	if err == nil {
		t.Fatal("expected error for non-struct type")
	}
}

func TestSearchBuilder_Chaining(t *testing.T) {
	idx, err := NewIndex[textDoc](nil, "test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	b := idx.Search().
		Query("hello world").
		Mode(ModeSemantic).
		Where("author", "alice").
		Limit(20)

	if b.query != "hello world" {
		t.Errorf("query = %q, want hello world", b.query)
	}
	if b.mode != ModeSemantic {
		t.Errorf("mode = %q, want semantic", b.mode)
	}
	if b.limit != 20 {
		t.Errorf("limit = %d, want 20", b.limit)
	}
	if len(b.filters) != 1 {
		t.Fatalf("len(filters) = %d, want 1", len(b.filters))
	}
	if b.filters[0].Key != "author" || b.filters[0].Match != "alice" {
		t.Errorf("filter = %+v", b.filters[0])
	}
}

func TestSearchBuilder_ToHits_Text(t *testing.T) {
	idx, err := NewIndex[textDoc](nil, "test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	results := []SearchResult{
		{
			ID:      "doc-1",
			Score:   0.95,
			Content: "hello world",
			Tags:    map[string]string{"author": "test"},
			Numerics: map[string]float64{
				"priority": 42,
			},
		},
	}

	hits, err := idx.Search().toHits(results)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("len = %d, want 1", len(hits))
	}
	if hits[0].Item.ID != "doc-1" {
		t.Errorf("ID = %q, want doc-1", hits[0].Item.ID)
	}
	if hits[0].Item.Content != "hello world" {
		t.Errorf("Content = %q, want hello world", hits[0].Item.Content)
	}
	if hits[0].Item.Author != "test" {
		t.Errorf("Author = %q, want test", hits[0].Item.Author)
	}
	if hits[0].Item.Priority != 42 {
		t.Errorf("Priority = %d, want 42", hits[0].Item.Priority)
	}
	if hits[0].Score != 0.95 {
		t.Errorf("Score = %f, want 0.95", hits[0].Score)
	}
}

func TestSearchBuilder_ToHits_Empty(t *testing.T) {
	idx, err := NewIndex[textDoc](nil, "test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	hits, err := idx.Search().toHits(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(hits) != 0 {
		t.Errorf("len = %d, want 0", len(hits))
	}
}

func newTypedIndex(t *testing.T, client *Client) *TypedIndex[textDoc] {
	t.Helper()
	idx, err := NewIndex[textDoc](client, "articles")
	if err != nil {
		t.Fatalf("NewIndex: %v", err)
	}
	return idx
}

func newNoopCollectionUC() *mockCollectionUC {
	return &mockCollectionUC{
		createFn: func(_ context.Context, _ string, _ domcol.Type, _ []field.Field) (domcol.Collection, error) {
			return domcol.Collection{}, nil
		},
		getFn:    func(_ context.Context, _ string) (domcol.Collection, error) { return domcol.Collection{}, nil },
		listFn:   func(_ context.Context) ([]domcol.Collection, error) { return nil, nil },
		deleteFn: func(_ context.Context, _ string) error { return nil },
	}
}

func newNoopDocumentUC() *mockDocumentUC {
	return &mockDocumentUC{
		upsertFn: func(_ context.Context, _ string, _ *domdoc.Document) (bool, error) { return false, nil },
		getFn:    func(_ context.Context, _, _ string) (domdoc.Document, error) { return domdoc.Document{}, nil },
		listFn:   func(_ context.Context, _, _ string, _ int) ([]domdoc.Document, string, error) { return nil, "", nil },
		deleteFn: func(_ context.Context, _, _ string) error { return nil },
		patchFn: func(_ context.Context, _, _ string, _ patch.Patch) (domdoc.Document, error) {
			return domdoc.Document{}, nil
		},
		countFn: func(_ context.Context, _ string) (int, error) { return 0, nil },
	}
}

func newNoopBatchUC() *mockBatchUC {
	return &mockBatchUC{
		upsertFn: func(_ context.Context, _ string, _ []domdoc.Document) []dombatch.Result { return nil },
		deleteFn: func(_ context.Context, _ string, _ []string) []dombatch.Result { return nil },
	}
}

func TestTypedIndex_Ensure(t *testing.T) {
	f, err := field.New("author", field.Tag)
	if err != nil {
		t.Fatalf("field.New: %v", err)
	}
	col := domcol.Reconstruct("articles", domcol.TypeText, []field.Field{f}, 1024, 1000, 1)
	client := testClient(
		&mockCollectionUC{
			createFn: func(_ context.Context, name string, _ domcol.Type, _ []field.Field) (domcol.Collection, error) {
				if name != "articles" {
					t.Fatalf("Create name = %q, want articles", name)
				}
				return col, nil
			},
			getFn: func(_ context.Context, name string) (domcol.Collection, error) {
				return col, nil
			},
			listFn:   func(_ context.Context) ([]domcol.Collection, error) { return nil, nil },
			deleteFn: func(_ context.Context, _ string) error { return nil },
		},
		&mockDocumentUC{
			upsertFn: func(_ context.Context, _ string, _ *domdoc.Document) (bool, error) { return false, nil },
			getFn:    func(_ context.Context, _, _ string) (domdoc.Document, error) { return domdoc.Document{}, nil },
			listFn:   func(_ context.Context, _, _ string, _ int) ([]domdoc.Document, string, error) { return nil, "", nil },
			deleteFn: func(_ context.Context, _, _ string) error { return nil },
			patchFn: func(_ context.Context, _, _ string, _ patch.Patch) (domdoc.Document, error) {
				return domdoc.Document{}, nil
			},
			countFn: func(_ context.Context, _ string) (int, error) { return 0, nil },
		},
		&mockBatchUC{
			upsertFn: func(_ context.Context, _ string, _ []domdoc.Document) []dombatch.Result { return nil },
			deleteFn: func(_ context.Context, _ string, _ []string) []dombatch.Result { return nil },
		},
		&mockSearchUC{
			searchFn: func(_ context.Context, _ string, _ *request.Request) ([]result.Result, int, error) {
				return nil, 0, nil
			},
		},
	)
	idx := newTypedIndex(t, client)
	if err := idx.Ensure(context.Background()); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
}

func TestTypedIndex_Ensure_Error(t *testing.T) {
	client := testClient(
		&mockCollectionUC{
			createFn: func(_ context.Context, _ string, _ domcol.Type, _ []field.Field) (domcol.Collection, error) {
				return domcol.Collection{}, errors.New("boom")
			},
			getFn:    func(_ context.Context, _ string) (domcol.Collection, error) { return domcol.Collection{}, nil },
			listFn:   func(_ context.Context) ([]domcol.Collection, error) { return nil, nil },
			deleteFn: func(_ context.Context, _ string) error { return nil },
		},
		&mockDocumentUC{
			upsertFn: func(_ context.Context, _ string, _ *domdoc.Document) (bool, error) { return false, nil },
			getFn:    func(_ context.Context, _, _ string) (domdoc.Document, error) { return domdoc.Document{}, nil },
			listFn:   func(_ context.Context, _, _ string, _ int) ([]domdoc.Document, string, error) { return nil, "", nil },
			deleteFn: func(_ context.Context, _, _ string) error { return nil },
			patchFn: func(_ context.Context, _, _ string, _ patch.Patch) (domdoc.Document, error) {
				return domdoc.Document{}, nil
			},
			countFn: func(_ context.Context, _ string) (int, error) { return 0, nil },
		},
		&mockBatchUC{
			upsertFn: func(_ context.Context, _ string, _ []domdoc.Document) []dombatch.Result { return nil },
			deleteFn: func(_ context.Context, _ string, _ []string) []dombatch.Result { return nil },
		},
		&mockSearchUC{
			searchFn: func(_ context.Context, _ string, _ *request.Request) ([]result.Result, int, error) {
				return nil, 0, nil
			},
		},
	)
	idx := newTypedIndex(t, client)
	if err := idx.Ensure(context.Background()); err == nil {
		t.Fatal("expected error")
	}
}

func TestTypedIndex_Upsert(t *testing.T) {
	doc := textDoc{ID: "doc-1", Content: "hello valkey", Author: "alice", Priority: 7}
	documentUC := newNoopDocumentUC()
	documentUC.upsertFn = func(_ context.Context, collection string, got *domdoc.Document) (bool, error) {
		if collection != "articles" || got.ID() != "doc-1" {
			t.Fatalf("Upsert(%q, %q)", collection, got.ID())
		}
		return true, nil
	}
	idx := newTypedIndex(t, testClient(newNoopCollectionUC(), documentUC, newNoopBatchUC(), &mockSearchUC{}))
	created, err := idx.Upsert(context.Background(), doc)
	if err != nil || !created {
		t.Fatalf("Upsert created=%v err=%v", created, err)
	}
}

func TestTypedIndex_UpsertBatch(t *testing.T) {
	doc := textDoc{ID: "doc-1", Content: "hello valkey", Author: "alice", Priority: 7}
	batchUC := newNoopBatchUC()
	batchUC.upsertFn = func(_ context.Context, collection string, docs []domdoc.Document) []dombatch.Result {
		if collection != "articles" || len(docs) != 1 || docs[0].ID() != "doc-1" {
			t.Fatalf("BatchUpsert(%q, %+v)", collection, docs)
		}
		return []dombatch.Result{dombatch.NewOK("doc-1")}
	}
	idx := newTypedIndex(t, testClient(newNoopCollectionUC(), newNoopDocumentUC(), batchUC, &mockSearchUC{}))
	batchResp, err := idx.UpsertBatch(context.Background(), []textDoc{doc})
	if err != nil {
		t.Fatalf("UpsertBatch: %v", err)
	}
	if len(batchResp.Results) != 1 || !batchResp.Results[0].OK {
		t.Fatalf("unexpected batch response: %+v", batchResp)
	}
}

func TestTypedIndex_GetCountDelete(t *testing.T) {
	doc, err := domdoc.New(
		"doc-1",
		"hello valkey",
		map[string]string{"author": "alice"},
		map[string]float64{"priority": 7},
	)
	if err != nil {
		t.Fatalf("domdoc.New: %v", err)
	}
	documentUC := newNoopDocumentUC()
	documentUC.getFn = func(_ context.Context, collection, id string) (domdoc.Document, error) {
		if collection != "articles" || id != "doc-1" {
			t.Fatalf("Get(%q, %q)", collection, id)
		}
		return doc, nil
	}
	documentUC.countFn = func(_ context.Context, collection string) (int, error) {
		if collection != "articles" {
			t.Fatalf("Count collection = %q, want articles", collection)
		}
		return 1, nil
	}
	documentUC.deleteFn = func(_ context.Context, collection, id string) error {
		if collection != "articles" || id != "doc-1" {
			t.Fatalf("Delete(%q, %q)", collection, id)
		}
		return nil
	}
	idx := newTypedIndex(t, testClient(newNoopCollectionUC(), documentUC, newNoopBatchUC(), &mockSearchUC{}))
	got, err := idx.Get(context.Background(), "doc-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.ID != "doc-1" || got.Author != "alice" || got.Priority != 7 {
		t.Fatalf("unexpected item: %+v", got)
	}
	total, err := idx.Count(context.Background())
	if err != nil || total != 1 {
		t.Fatalf("Count total=%d err=%v", total, err)
	}
	if err := idx.Delete(context.Background(), "doc-1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
}

func TestTypedIndex_Get_Error(t *testing.T) {
	client := testClient(
		&mockCollectionUC{},
		&mockDocumentUC{
			upsertFn: func(_ context.Context, _ string, _ *domdoc.Document) (bool, error) { return false, nil },
			getFn: func(_ context.Context, _, _ string) (domdoc.Document, error) {
				return domdoc.Document{}, errors.New("boom")
			},
			listFn:   func(_ context.Context, _, _ string, _ int) ([]domdoc.Document, string, error) { return nil, "", nil },
			deleteFn: func(_ context.Context, _, _ string) error { return nil },
			patchFn: func(_ context.Context, _, _ string, _ patch.Patch) (domdoc.Document, error) {
				return domdoc.Document{}, nil
			},
			countFn: func(_ context.Context, _ string) (int, error) { return 0, nil },
		},
		newNoopBatchUC(),
		&mockSearchUC{},
	)
	idx := newTypedIndex(t, client)
	if _, err := idx.Get(context.Background(), "doc-1"); err == nil {
		t.Fatal("expected error")
	}
}

func TestTypedIndex_SearchDo(t *testing.T) {
	client := testClient(
		&mockCollectionUC{},
		&mockDocumentUC{},
		&mockBatchUC{},
		&mockSearchUC{
			searchFn: func(_ context.Context, collection string, req *request.Request) ([]result.Result, int, error) {
				if collection != "articles" || req.Query() != "hello" {
					t.Fatalf("Search(%q, %q)", collection, req.Query())
				}
				return []result.Result{
					result.New(
						"doc-1",
						0.99,
						"hello valkey",
						map[string]string{"author": "alice"},
						map[string]float64{"priority": 7},
						nil,
					),
				}, 1, nil
			},
		},
	)
	idx := newTypedIndex(t, client)
	hits, err := idx.Search().Query("hello").Mode(ModeHybrid).Limit(5).Do(context.Background())
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if len(hits) != 1 || hits[0].Item.ID != "doc-1" {
		t.Fatalf("unexpected hits: %+v", hits)
	}
}

func TestTypedIndex_SearchDo_Error(t *testing.T) {
	client := testClient(
		&mockCollectionUC{},
		&mockDocumentUC{},
		&mockBatchUC{},
		&mockSearchUC{
			searchFn: func(_ context.Context, _ string, _ *request.Request) ([]result.Result, int, error) {
				return nil, 0, errors.New("boom")
			},
		},
	)
	idx := newTypedIndex(t, client)
	if _, err := idx.Search().Query("hello").Do(context.Background()); err == nil {
		t.Fatal("expected error")
	}
}

func TestWithStandalone(t *testing.T) {
	cfg := &clientConfig{}
	WithStandalone().apply(cfg)
	if !cfg.standalone {
		t.Fatal("expected standalone=true")
	}
}
