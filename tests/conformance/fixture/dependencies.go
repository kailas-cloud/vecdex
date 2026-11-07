package main

import (
	"context"

	"github.com/kailas-cloud/vecdex/internal/domain"
	domcol "github.com/kailas-cloud/vecdex/internal/domain/collection"
	"github.com/kailas-cloud/vecdex/internal/domain/collection/field"
	domdoc "github.com/kailas-cloud/vecdex/internal/domain/document"
	"github.com/kailas-cloud/vecdex/internal/domain/document/patch"
	"github.com/kailas-cloud/vecdex/internal/domain/search/request"
	"github.com/kailas-cloud/vecdex/internal/domain/search/result"
	"github.com/kailas-cloud/vecdex/internal/memorycontext"
)

// These services deliberately do not simulate storage. This fixture tests the
// actual wire contract and shipped assets, never persistence or search quality.
type collections struct{}

func (collections) ListPage(context.Context, string, int) ([]domcol.Collection, string, error) {
	return []domcol.Collection{}, "", nil
}
func (collections) Get(context.Context, string) (domcol.Collection, error) {
	return domcol.Collection{}, domain.ErrNotFound
}
func (collections) Inspect(_ context.Context, name string) (domcol.Inspection, error) {
	return domcol.Inspection{Name: name, State: "absent"}, nil
}
func (collections) Create(context.Context, string, domcol.Type, []field.Field) (domcol.Collection, error) {
	return domcol.Collection{}, domain.ErrNotImplemented
}
func (collections) DeleteIfGeneration(context.Context, string, string) error {
	return domain.ErrNotImplemented
}

type documents struct{}

func (documents) ReadSource(context.Context, string, string, domdoc.SourceProjection) (domdoc.Document, error) {
	return domdoc.Document{}, domain.ErrDocumentNotFound
}
func (documents) ListSources(
	context.Context, string, string, int, domdoc.SourceProjection,
) ([]domdoc.Document, string, error) {
	return []domdoc.Document{}, "", nil
}
func (documents) Upsert(context.Context, string, *domdoc.Document) (bool, error) {
	return false, domain.ErrNotImplemented
}
func (documents) Patch(context.Context, string, string, patch.Patch) (domdoc.WriteResult, error) {
	return domdoc.WriteResult{}, domain.ErrNotImplemented
}
func (documents) Delete(context.Context, string, string) error { return domain.ErrNotImplemented }

type search struct{}

func (search) Search(context.Context, string, *request.Request) ([]result.Result, int, error) {
	return nil, 0, domain.ErrNotImplemented
}
func (search) Similar(context.Context, string, string, *request.SimilarRequest) ([]result.Result, int, error) {
	return nil, 0, domain.ErrNotImplemented
}

type contexts struct{}

func (contexts) Get(context.Context, string, string) (memorycontext.Context, error) {
	return memorycontext.Context{}, memorycontext.ErrNotFound
}
func (contexts) List(context.Context, memorycontext.ListRequest) (memorycontext.Page, error) {
	return memorycontext.Page{Contexts: []memorycontext.Context{}}, nil
}
func (contexts) Create(context.Context, memorycontext.CreateRequest) (memorycontext.MutationResult, error) {
	return memorycontext.MutationResult{}, domain.ErrNotImplemented
}
func (contexts) CreateValidated(
	context.Context, memorycontext.CreateRequest, func(context.Context) error,
) (memorycontext.MutationResult, error) {
	return memorycontext.MutationResult{}, domain.ErrNotImplemented
}
func (contexts) Update(context.Context, memorycontext.UpdateRequest) (memorycontext.MutationResult, error) {
	return memorycontext.MutationResult{}, domain.ErrNotImplemented
}
func (contexts) Delete(context.Context, memorycontext.DeleteRequest) (memorycontext.MutationResult, error) {
	return memorycontext.MutationResult{}, domain.ErrNotImplemented
}
