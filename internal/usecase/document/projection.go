package document

import (
	"context"
	"fmt"

	"github.com/kailas-cloud/vecdex/internal/domain"
	domdoc "github.com/kailas-cloud/vecdex/internal/domain/document"
)

type sourceReader interface {
	ReadSource(context.Context, string, string, domdoc.SourceProjection) (domdoc.Document, error)
	ListSources(context.Context, string, string, int, domdoc.SourceProjection) ([]domdoc.Document, string, error)
}

// ReadSource returns selected source fields without loading private fragments.
func (s *Service) ReadSource(
	ctx context.Context, collection, id string, projection domdoc.SourceProjection,
) (domdoc.Document, error) {
	col, err := s.resolveCollection(ctx, collection)
	if err != nil {
		return domdoc.Document{}, fmt.Errorf("get collection: %w", err)
	}
	repo, ok := s.repo.(sourceReader)
	if !ok {
		return domdoc.Document{}, domain.ErrNotImplemented
	}
	doc, err := repo.ReadSource(domain.WithCollectionGeneration(ctx, col.Generation()), collection, id, projection)
	if err != nil {
		return domdoc.Document{}, fmt.Errorf("read source: %w", err)
	}
	return doc, nil
}

// ListSources returns a bounded live page with the selected source fields.
func (s *Service) ListSources(
	ctx context.Context, collection, cursor string, limit int, projection domdoc.SourceProjection,
) ([]domdoc.Document, string, error) {
	col, err := s.resolveCollection(ctx, collection)
	if err != nil {
		return nil, "", fmt.Errorf("get collection: %w", err)
	}
	if limit == 0 {
		limit = min(s.defaultPageSize, 100)
	}
	if limit < 1 || limit > min(s.maxPageSize, 100) {
		return nil, "", fmt.Errorf("invalid source page size: %w", domain.ErrInvalidSchema)
	}
	repo, ok := s.repo.(sourceReader)
	if !ok {
		return nil, "", domain.ErrNotImplemented
	}
	ctx = domain.WithCollectionGeneration(ctx, col.Generation())
	docs, next, err := repo.ListSources(ctx, collection, cursor, limit, projection)
	if err != nil {
		return nil, "", fmt.Errorf("list source projections: %w", err)
	}
	return docs, next, nil
}
