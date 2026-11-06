package vecdex

import (
	"context"
	"fmt"

	"github.com/kailas-cloud/vecdex/internal/domain"
	domdoc "github.com/kailas-cloud/vecdex/internal/domain/document"
)

type sourceUseCase interface {
	ReadSource(context.Context, string, string, domdoc.SourceProjection) (domdoc.Document, error)
	ListSources(context.Context, string, string, int, domdoc.SourceProjection) ([]domdoc.Document, string, error)
}

// ReadSource reads selected parent fields without loading vectors.
func (s *DocumentService) ReadSource(ctx context.Context, id string, projection SourceProjection) (Document, error) {
	reader, ok := s.docSvc.(sourceUseCase)
	if !ok {
		return Document{}, domain.ErrNotImplemented
	}
	selection := domdoc.SourceProjection{Content: projection.Content, Metadata: projection.Metadata}
	doc, err := reader.ReadSource(ctx, s.collection, id, selection)
	if err != nil {
		return Document{}, fmt.Errorf("read source: %w", err)
	}
	return fromInternalDocument(&doc), nil
}

// ListSources returns a live page of selected parent fields.
func (s *DocumentService) ListSources(
	ctx context.Context, cursor string, limit int, projection SourceProjection,
) (ListResult, error) {
	reader, ok := s.docSvc.(sourceUseCase)
	if !ok {
		return ListResult{}, domain.ErrNotImplemented
	}
	selection := domdoc.SourceProjection{Content: projection.Content, Metadata: projection.Metadata}
	docs, next, err := reader.ListSources(ctx, s.collection, cursor, limit, selection)
	if err != nil {
		return ListResult{}, fmt.Errorf("list sources: %w", err)
	}
	out := make([]Document, len(docs))
	for i := range docs {
		out[i] = fromInternalDocument(&docs[i])
	}
	return ListResult{Documents: out, NextCursor: next}, nil
}
