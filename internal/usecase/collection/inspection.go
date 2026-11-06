package collection

import (
	"context"
	"fmt"

	"github.com/kailas-cloud/vecdex/internal/domain"
	domcol "github.com/kailas-cloud/vecdex/internal/domain/collection"
)

type pageReader interface {
	ListPage(context.Context, string, int) ([]domcol.Collection, string, error)
}

type inspector interface {
	Inspect(context.Context, string) (domcol.Inspection, error)
}

// ListPage returns a bounded live catalog page in lexicographic name order.
// Continue while the cursor is nonempty, even if a page contains no active collections.
func (s *Service) ListPage(ctx context.Context, cursor string, limit int) ([]domcol.Collection, string, error) {
	if limit == 0 {
		limit = 20
	}
	if limit < 1 || limit > 100 {
		return nil, "", fmt.Errorf("collection page size must be 1–100: %w", domain.ErrInvalidSchema)
	}
	repo, ok := s.repo.(pageReader)
	if !ok {
		return nil, "", domain.ErrNotImplemented
	}
	cols, next, err := repo.ListPage(ctx, cursor, limit)
	if err != nil {
		return nil, "", fmt.Errorf("list collection page: %w", err)
	}
	return cols, next, nil
}

// Inspect returns active and unfinished collection lifecycle states, or absent.
func (s *Service) Inspect(ctx context.Context, name string) (domcol.Inspection, error) {
	repo, ok := s.repo.(inspector)
	if !ok {
		return domcol.Inspection{}, domain.ErrNotImplemented
	}
	inspection, err := repo.Inspect(ctx, name)
	if err != nil {
		return inspection, fmt.Errorf("inspect collection: %w", err)
	}
	if inspection.State != "absent" {
		if err := domain.ValidateCollectionGeneration(ctx, inspection.Generation); err != nil {
			return domcol.Inspection{}, fmt.Errorf("inspect collection generation: %w", err)
		}
	}
	return inspection, nil
}

// DeleteIfGeneration deletes only the named incarnation, never its replacement.
func (s *Service) DeleteIfGeneration(ctx context.Context, name, generation string) error {
	if generation == "" {
		return fmt.Errorf("collection generation is required: %w", domain.ErrInvalidSchema)
	}
	return s.Delete(domain.WithExpectedCollectionGeneration(ctx, generation), name)
}
