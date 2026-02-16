package collection

import (
	"context"
	"fmt"

	"github.com/kailas-cloud/vecdex/internal/domain"
	domcol "github.com/kailas-cloud/vecdex/internal/domain/collection"
	"github.com/kailas-cloud/vecdex/internal/domain/collection/field"
)

// Service handles collection CRUD operations.
type Service struct {
	repo      Repository
	vectorDim int
}

// New creates a collection service.
func New(repo Repository, vectorDim int) *Service {
	return &Service{repo: repo, vectorDim: vectorDim}
}

// Create validates and stores a new collection.
func (s *Service) Create(ctx context.Context, name string, colType domcol.Type, fields []field.Field) (domcol.Collection, error) {
	col, err := domcol.New(name, colType, fields, s.vectorDim)
	if err != nil {
		return domcol.Collection{}, fmt.Errorf("validate collection: %w: %w", domain.ErrInvalidSchema, err)
	}

	if err := s.repo.Create(ctx, col); err != nil {
		return domcol.Collection{}, fmt.Errorf("create collection: %w", err)
	}

	return col, nil
}

// Get retrieves a collection by name.
func (s *Service) Get(ctx context.Context, name string) (domcol.Collection, error) {
	col, err := s.repo.Get(ctx, name)
	if err != nil {
		return domcol.Collection{}, fmt.Errorf("get collection: %w", err)
	}
	return col, nil
}

// List returns all collections.
func (s *Service) List(ctx context.Context) ([]domcol.Collection, error) {
	cols, err := s.repo.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("list collections: %w", err)
	}
	return cols, nil
}

// Delete removes a collection.
func (s *Service) Delete(ctx context.Context, name string) error {
	if err := s.repo.Delete(ctx, name); err != nil {
		return fmt.Errorf("delete collection: %w", err)
	}
	return nil
}
