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

// --- CollectionService ---

func TestCollectionService_Create(t *testing.T) {
	f, _ := field.New("country", field.Tag)
	col := domcol.Reconstruct("places", domcol.TypeGeo, []field.Field{f}, 3, 1000, 1)

	mock := &mockCollectionUC{
		createFn: func(_ context.Context, name string, _ domcol.Type, _ []field.Field) (domcol.Collection, error) {
			if name != "places" {
				t.Errorf("name = %q, want places", name)
			}
			return col, nil
		},
	}

	svc := &CollectionService{svc: mock}
	info, err := svc.Create(context.Background(), "places", Geo(), WithField("country", FieldTag))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if info.Name != "places" {
		t.Errorf("Name = %q, want places", info.Name)
	}
}

func TestCollectionService_Create_Error(t *testing.T) {
	mock := &mockCollectionUC{
		createFn: func(_ context.Context, _ string, _ domcol.Type, _ []field.Field) (domcol.Collection, error) {
			return domcol.Collection{}, errors.New("db down")
		},
	}

	svc := &CollectionService{svc: mock}
	_, err := svc.Create(context.Background(), "test")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestCollectionService_Get(t *testing.T) {
	col := domcol.Reconstruct("test", domcol.TypeText, nil, 1024, 2000, 1)
	mock := &mockCollectionUC{
		getFn: func(_ context.Context, name string) (domcol.Collection, error) {
			return col, nil
		},
	}

	svc := &CollectionService{svc: mock}
	info, err := svc.Get(context.Background(), "test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if info.Name != "test" {
		t.Errorf("Name = %q, want test", info.Name)
	}
}

func TestCollectionService_Get_Error(t *testing.T) {
	mock := &mockCollectionUC{
		getFn: func(_ context.Context, _ string) (domcol.Collection, error) {
			return domcol.Collection{}, errors.New("not found")
		},
	}

	svc := &CollectionService{svc: mock}
	_, err := svc.Get(context.Background(), "x")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestCollectionService_List(t *testing.T) {
	col := domcol.Reconstruct("a", domcol.TypeText, nil, 1024, 1000, 1)
	mock := &mockCollectionUC{
		listFn: func(_ context.Context) ([]domcol.Collection, error) {
			return []domcol.Collection{col}, nil
		},
	}

	svc := &CollectionService{svc: mock}
	list, err := svc.List(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("len = %d, want 1", len(list))
	}
}

func TestCollectionService_List_Error(t *testing.T) {
	mock := &mockCollectionUC{
		listFn: func(_ context.Context) ([]domcol.Collection, error) {
			return nil, errors.New("fail")
		},
	}

	svc := &CollectionService{svc: mock}
	_, err := svc.List(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestCollectionService_Delete(t *testing.T) {
	mock := &mockCollectionUC{
		deleteFn: func(_ context.Context, _ string) error { return nil },
	}
	svc := &CollectionService{svc: mock}
	if err := svc.Delete(context.Background(), "x"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCollectionService_Delete_Error(t *testing.T) {
	mock := &mockCollectionUC{
		deleteFn: func(_ context.Context, _ string) error { return errors.New("fail") },
	}
	svc := &CollectionService{svc: mock}
	if err := svc.Delete(context.Background(), "x"); err == nil {
		t.Fatal("expected error")
	}
}

func TestCollectionService_Ensure_New(t *testing.T) {
	col := domcol.Reconstruct("test", domcol.TypeGeo, nil, 3, 1000, 1)
	mock := &mockCollectionUC{
		createFn: func(_ context.Context, _ string, _ domcol.Type, _ []field.Field) (domcol.Collection, error) {
			return col, nil
		},
	}

	svc := &CollectionService{svc: mock}
	info, err := svc.Ensure(context.Background(), "test", Geo())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if info.Name != "test" {
		t.Errorf("Name = %q, want test", info.Name)
	}
}

func TestCollectionService_Ensure_Exists(t *testing.T) {
	col := domcol.Reconstruct("test", domcol.TypeGeo, nil, 3, 1000, 1)
	mock := &mockCollectionUC{
		createFn: func(_ context.Context, _ string, _ domcol.Type, _ []field.Field) (domcol.Collection, error) {
			return domcol.Collection{}, ErrAlreadyExists
		},
		getFn: func(_ context.Context, _ string) (domcol.Collection, error) {
			return col, nil
		},
	}

	svc := &CollectionService{svc: mock}
	info, err := svc.Ensure(context.Background(), "test", Geo())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if info.Name != "test" {
		t.Errorf("Name = %q, want test", info.Name)
	}
}

func TestCollectionService_Ensure_OtherError(t *testing.T) {
	mock := &mockCollectionUC{
		createFn: func(_ context.Context, _ string, _ domcol.Type, _ []field.Field) (domcol.Collection, error) {
			return domcol.Collection{}, errors.New("db down")
		},
	}

	svc := &CollectionService{svc: mock}
	_, err := svc.Ensure(context.Background(), "test")
	if err == nil {
		t.Fatal("expected error")
	}
}

// --- DocumentService ---

func newDocSvc(docUC *mockDocumentUC, batchUC *mockBatchUC) *DocumentService {
	return &DocumentService{collection: "test", docSvc: docUC, batchSvc: batchUC}
}

func TestDocumentService_Upsert(t *testing.T) {
	mock := &mockDocumentUC{
		upsertFn: func(_ context.Context, _ string, doc *domdoc.Document) (bool, error) {
			if doc.ID() != "doc-1" {
				t.Errorf("ID = %q, want doc-1", doc.ID())
			}
			return true, nil
		},
	}

	svc := newDocSvc(mock, nil)
	created, err := svc.Upsert(context.Background(), Document{ID: "doc-1", Content: "hi"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !created {
		t.Error("expected created=true")
	}
}

func TestDocumentService_Upsert_ServiceError(t *testing.T) {
	mock := &mockDocumentUC{
		upsertFn: func(_ context.Context, _ string, _ *domdoc.Document) (bool, error) {
			return false, errors.New("fail")
		},
	}

	svc := newDocSvc(mock, nil)
	_, err := svc.Upsert(context.Background(), Document{ID: "doc-1"})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestDocumentService_Get(t *testing.T) {
	d, _ := domdoc.New("doc-1", "hello", nil, nil)
	mock := &mockDocumentUC{
		getFn: func(_ context.Context, _, id string) (domdoc.Document, error) {
			return d, nil
		},
	}

	svc := newDocSvc(mock, nil)
	doc, err := svc.Get(context.Background(), "doc-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if doc.ID != "doc-1" {
		t.Errorf("ID = %q, want doc-1", doc.ID)
	}
}

func TestDocumentService_Get_Error(t *testing.T) {
	mock := &mockDocumentUC{
		getFn: func(_ context.Context, _, _ string) (domdoc.Document, error) {
			return domdoc.Document{}, errors.New("not found")
		},
	}

	svc := newDocSvc(mock, nil)
	_, err := svc.Get(context.Background(), "x")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestDocumentService_List(t *testing.T) {
	d, _ := domdoc.New("doc-1", "hi", nil, nil)
	mock := &mockDocumentUC{
		listFn: func(_ context.Context, _, _ string, _ int) ([]domdoc.Document, string, error) {
			return []domdoc.Document{d}, "next-cursor", nil
		},
	}

	svc := newDocSvc(mock, nil)
	lr, err := svc.List(context.Background(), "", 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(lr.Documents) != 1 {
		t.Fatalf("len = %d, want 1", len(lr.Documents))
	}
	if lr.NextCursor != "next-cursor" {
		t.Errorf("cursor = %q, want next-cursor", lr.NextCursor)
	}
}

func TestDocumentService_List_Error(t *testing.T) {
	mock := &mockDocumentUC{
		listFn: func(_ context.Context, _, _ string, _ int) ([]domdoc.Document, string, error) {
			return nil, "", errors.New("fail")
		},
	}

	svc := newDocSvc(mock, nil)
	_, err := svc.List(context.Background(), "", 10)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestDocumentService_Delete(t *testing.T) {
	mock := &mockDocumentUC{
		deleteFn: func(_ context.Context, _, _ string) error { return nil },
	}
	svc := newDocSvc(mock, nil)
	if err := svc.Delete(context.Background(), "x"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestDocumentService_Delete_Error(t *testing.T) {
	mock := &mockDocumentUC{
		deleteFn: func(_ context.Context, _, _ string) error { return errors.New("fail") },
	}
	svc := newDocSvc(mock, nil)
	if err := svc.Delete(context.Background(), "x"); err == nil {
		t.Fatal("expected error")
	}
}

func TestDocumentService_Patch(t *testing.T) {
	d, _ := domdoc.New("doc-1", "updated", nil, nil)
	mock := &mockDocumentUC{
		patchFn: func(_ context.Context, _, _ string, _ patch.Patch) (domdoc.Document, error) {
			return d, nil
		},
	}

	content := "updated"
	svc := newDocSvc(mock, nil)
	doc, err := svc.Patch(context.Background(), "doc-1", DocumentPatch{Content: &content})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if doc.Content != "updated" {
		t.Errorf("Content = %q, want updated", doc.Content)
	}
}

func TestDocumentService_Patch_Error(t *testing.T) {
	mock := &mockDocumentUC{
		patchFn: func(_ context.Context, _, _ string, _ patch.Patch) (domdoc.Document, error) {
			return domdoc.Document{}, errors.New("fail")
		},
	}

	content := "x"
	svc := newDocSvc(mock, nil)
	_, err := svc.Patch(context.Background(), "doc-1", DocumentPatch{Content: &content})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestDocumentService_Count(t *testing.T) {
	mock := &mockDocumentUC{
		countFn: func(_ context.Context, _ string) (int, error) { return 42, nil },
	}
	svc := newDocSvc(mock, nil)
	n, err := svc.Count(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n != 42 {
		t.Errorf("count = %d, want 42", n)
	}
}

func TestDocumentService_Count_Error(t *testing.T) {
	mock := &mockDocumentUC{
		countFn: func(_ context.Context, _ string) (int, error) { return 0, errors.New("fail") },
	}
	svc := newDocSvc(mock, nil)
	_, err := svc.Count(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestDocumentService_BatchUpsert(t *testing.T) {
	batch := &mockBatchUC{
		upsertFn: func(_ context.Context, _ string, docs []domdoc.Document) []dombatch.Result {
			return []dombatch.Result{dombatch.NewOK(docs[0].ID())}
		},
	}

	svc := newDocSvc(nil, batch)
	results, err := svc.BatchUpsert(context.Background(), []Document{{ID: "doc-1"}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 || !results[0].OK {
		t.Errorf("results = %+v", results)
	}
}

func TestDocumentService_BatchDelete(t *testing.T) {
	batch := &mockBatchUC{
		deleteFn: func(_ context.Context, _ string, ids []string) []dombatch.Result {
			return []dombatch.Result{dombatch.NewOK(ids[0])}
		},
	}

	svc := newDocSvc(nil, batch)
	results := svc.BatchDelete(context.Background(), []string{"doc-1"})
	if len(results) != 1 || !results[0].OK {
		t.Errorf("results = %+v", results)
	}
}

// --- SearchService ---

func TestSearchService_Query(t *testing.T) {
	mock := &mockSearchUC{
		searchFn: func(_ context.Context, _ string, _ *request.Request) ([]result.Result, int, error) {
			return []result.Result{result.New("doc-1", 0.9, "hi", nil, nil, nil)}, 1, nil
		},
	}

	svc := &SearchService{collection: "test", svc: mock}
	resp, err := svc.Query(context.Background(), "hello", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.Results) != 1 {
		t.Fatalf("len = %d, want 1", len(resp.Results))
	}
	if resp.Results[0].ID != "doc-1" {
		t.Errorf("ID = %q, want doc-1", resp.Results[0].ID)
	}
	if resp.Total != 1 {
		t.Errorf("Total = %d, want 1", resp.Total)
	}
}

func TestSearchService_Query_WithOpts(t *testing.T) {
	mock := &mockSearchUC{
		searchFn: func(_ context.Context, _ string, _ *request.Request) ([]result.Result, int, error) {
			return nil, 0, nil
		},
	}

	svc := &SearchService{collection: "test", svc: mock}
	opts := &SearchOptions{Mode: ModeSemantic, Limit: 5}
	_, err := svc.Query(context.Background(), "hello", opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestSearchService_Query_Error(t *testing.T) {
	mock := &mockSearchUC{
		searchFn: func(_ context.Context, _ string, _ *request.Request) ([]result.Result, int, error) {
			return nil, 0, errors.New("fail")
		},
	}

	svc := &SearchService{collection: "test", svc: mock}
	_, err := svc.Query(context.Background(), "hello", nil)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestSearchService_Geo(t *testing.T) {
	mock := &mockSearchUC{
		searchFn: func(_ context.Context, _ string, _ *request.Request) ([]result.Result, int, error) {
			return []result.Result{result.New("place-1", 500, "", nil, nil, nil)}, 1, nil
		},
	}

	svc := &SearchService{collection: "test", svc: mock}
	resp, err := svc.Geo(context.Background(), 34.77, 32.42, 10, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.Results) != 1 {
		t.Fatalf("len = %d, want 1", len(resp.Results))
	}
}

func TestSearchService_Geo_WithOpts(t *testing.T) {
	mock := &mockSearchUC{
		searchFn: func(_ context.Context, _ string, _ *request.Request) ([]result.Result, int, error) {
			return nil, 0, nil
		},
	}

	svc := &SearchService{collection: "test", svc: mock}
	opts := &SearchOptions{
		Filters: FilterExpression{Must: []FilterCondition{{Key: "country", Match: "CY"}}},
	}
	_, err := svc.Geo(context.Background(), 34.77, 32.42, 10, opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestSearchService_Geo_Error(t *testing.T) {
	mock := &mockSearchUC{
		searchFn: func(_ context.Context, _ string, _ *request.Request) ([]result.Result, int, error) {
			return nil, 0, errors.New("fail")
		},
	}

	svc := &SearchService{collection: "test", svc: mock}
	_, err := svc.Geo(context.Background(), 34.77, 32.42, 10, nil)
	if err == nil {
		t.Fatal("expected error")
	}
}

// --- Client accessors ---

func TestClient_Accessors(t *testing.T) {
	c := testClient(&mockCollectionUC{}, &mockDocumentUC{}, &mockBatchUC{}, &mockSearchUC{})

	if c.Collections() == nil {
		t.Error("Collections() returned nil")
	}
	if c.Documents("test") == nil {
		t.Error("Documents() returned nil")
	}
	if c.Search("test") == nil {
		t.Error("Search() returned nil")
	}
}
