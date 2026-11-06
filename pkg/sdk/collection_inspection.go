package vecdex

import (
	"context"
	"fmt"

	"github.com/kailas-cloud/vecdex/internal/domain"
	domcol "github.com/kailas-cloud/vecdex/internal/domain/collection"
)

type collectionPageUseCase interface {
	ListPage(context.Context, string, int) ([]domcol.Collection, string, error)
	Inspect(context.Context, string) (domcol.Inspection, error)
	DeleteIfGeneration(context.Context, string, string) error
}

// ListPage returns a bounded lexicographic catalog page with an opaque cursor.
func (s *CollectionService) ListPage(ctx context.Context, cursor string, limit int) (CollectionListResult, error) {
	svc, ok := s.svc.(collectionPageUseCase)
	if !ok {
		return CollectionListResult{}, domain.ErrNotImplemented
	}
	cols, next, err := svc.ListPage(ctx, cursor, limit)
	if err != nil {
		return CollectionListResult{}, fmt.Errorf("list collection page: %w", err)
	}
	out := make([]CollectionInfo, len(cols))
	for i, col := range cols {
		out[i] = fromInternalCollection(col)
	}
	return CollectionListResult{Collections: out, NextCursor: next, HasMore: next != ""}, nil
}

// Inspect includes unfinished lifecycle states and explicit absence.
func (s *CollectionService) Inspect(ctx context.Context, name string) (CollectionInspection, error) {
	svc, ok := s.svc.(collectionPageUseCase)
	if !ok {
		return CollectionInspection{}, domain.ErrNotImplemented
	}
	state, err := svc.Inspect(ctx, name)
	if err != nil {
		return CollectionInspection{}, fmt.Errorf("inspect collection: %w", err)
	}
	out := CollectionInspection{Name: state.Name, Generation: state.Generation, State: state.State}
	if state.Collection != nil {
		col := fromInternalCollection(*state.Collection)
		out.Collection = &col
	}
	return out, nil
}

// DeleteIfGeneration never deletes a newer collection with the same name.
func (s *CollectionService) DeleteIfGeneration(ctx context.Context, name, generation string) error {
	svc, ok := s.svc.(collectionPageUseCase)
	if !ok {
		return domain.ErrNotImplemented
	}
	if err := svc.DeleteIfGeneration(ctx, name, generation); err != nil {
		return fmt.Errorf("delete collection generation: %w", err)
	}
	return nil
}
