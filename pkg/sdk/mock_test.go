package vecdex

import (
	"context"

	dombatch "github.com/kailas-cloud/vecdex/internal/domain/batch"
	domcol "github.com/kailas-cloud/vecdex/internal/domain/collection"
	"github.com/kailas-cloud/vecdex/internal/domain/collection/field"
	domdoc "github.com/kailas-cloud/vecdex/internal/domain/document"
	"github.com/kailas-cloud/vecdex/internal/domain/document/patch"
	"github.com/kailas-cloud/vecdex/internal/domain/search/request"
	"github.com/kailas-cloud/vecdex/internal/domain/search/result"
)

// --- collectionUseCase mock ---

type mockCollectionUC struct {
	createFn func(ctx context.Context, name string, colType domcol.Type, fields []field.Field) (domcol.Collection, error)
	getFn    func(ctx context.Context, name string) (domcol.Collection, error)
	listFn   func(ctx context.Context) ([]domcol.Collection, error)
	deleteFn func(ctx context.Context, name string) error
}

func (m *mockCollectionUC) Create(
	ctx context.Context, name string, colType domcol.Type, fields []field.Field,
) (domcol.Collection, error) {
	return m.createFn(ctx, name, colType, fields)
}

func (m *mockCollectionUC) Get(ctx context.Context, name string) (domcol.Collection, error) {
	return m.getFn(ctx, name)
}

func (m *mockCollectionUC) List(ctx context.Context) ([]domcol.Collection, error) {
	return m.listFn(ctx)
}

func (m *mockCollectionUC) Delete(ctx context.Context, name string) error {
	return m.deleteFn(ctx, name)
}

// --- documentUseCase mock ---

type mockDocumentUC struct {
	upsertFn func(ctx context.Context, col string, doc *domdoc.Document) (bool, error)
	getFn    func(ctx context.Context, col, id string) (domdoc.Document, error)
	listFn   func(ctx context.Context, col, cursor string, limit int) ([]domdoc.Document, string, error)
	deleteFn func(ctx context.Context, col, id string) error
	patchFn  func(ctx context.Context, col, id string, p patch.Patch) (domdoc.Document, error)
	countFn  func(ctx context.Context, col string) (int, error)
}

func (m *mockDocumentUC) Upsert(ctx context.Context, col string, doc *domdoc.Document) (bool, error) {
	return m.upsertFn(ctx, col, doc)
}

func (m *mockDocumentUC) Get(ctx context.Context, col, id string) (domdoc.Document, error) {
	return m.getFn(ctx, col, id)
}

func (m *mockDocumentUC) List(
	ctx context.Context, col, cursor string, limit int,
) ([]domdoc.Document, string, error) {
	return m.listFn(ctx, col, cursor, limit)
}

func (m *mockDocumentUC) Delete(ctx context.Context, col, id string) error {
	return m.deleteFn(ctx, col, id)
}

func (m *mockDocumentUC) Patch(ctx context.Context, col, id string, p patch.Patch) (domdoc.Document, error) {
	return m.patchFn(ctx, col, id, p)
}

func (m *mockDocumentUC) Count(ctx context.Context, col string) (int, error) {
	return m.countFn(ctx, col)
}

// --- batchUseCase mock ---

type mockBatchUC struct {
	upsertFn func(ctx context.Context, col string, docs []domdoc.Document) []dombatch.Result
	deleteFn func(ctx context.Context, col string, ids []string) []dombatch.Result
}

func (m *mockBatchUC) Upsert(ctx context.Context, col string, docs []domdoc.Document) []dombatch.Result {
	return m.upsertFn(ctx, col, docs)
}

func (m *mockBatchUC) Delete(ctx context.Context, col string, ids []string) []dombatch.Result {
	return m.deleteFn(ctx, col, ids)
}

// --- searchUseCase mock ---

type mockSearchUC struct {
	searchFn func(ctx context.Context, col string, req *request.Request) ([]result.Result, int, error)
}

func (m *mockSearchUC) Search(
	ctx context.Context, col string, req *request.Request,
) ([]result.Result, int, error) {
	return m.searchFn(ctx, col, req)
}

// --- helpers ---

func testClient(
	collSvc collectionUseCase,
	docSvc documentUseCase,
	batchSvc batchUseCase,
	searchSvc searchUseCase,
) *Client {
	return &Client{
		collSvc:   collSvc,
		docSvc:    docSvc,
		searchSvc: searchSvc,
		batchSvc:  batchSvc,
	}
}
