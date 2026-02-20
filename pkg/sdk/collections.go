package vecdex

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/kailas-cloud/vecdex/internal/domain"
	domcol "github.com/kailas-cloud/vecdex/internal/domain/collection"
	"github.com/kailas-cloud/vecdex/internal/domain/collection/field"
)

// CollectionService manages collections.
type CollectionService struct {
	svc collectionUseCase
	obs *observer
}

// Create creates a new collection.
func (s *CollectionService) Create(
	ctx context.Context, name string, opts ...CollectionOption,
) (_ CollectionInfo, err error) {
	start := time.Now()
	defer func() { s.obs.observe("collection.create", start, err) }()

	cfg := &collectionConfig{colType: CollectionTypeText}
	for _, o := range opts {
		o.applyCollection(cfg)
	}

	fields, err := toInternalFields(cfg.fields)
	if err != nil {
		return CollectionInfo{}, fmt.Errorf("create collection: %w", err)
	}

	col, err := s.svc.Create(ctx, name, domcol.Type(cfg.colType), fields)
	if err != nil {
		return CollectionInfo{}, fmt.Errorf("create collection: %w", err)
	}
	return fromInternalCollection(col), nil
}

// Ensure creates a collection if it does not exist.
// If it already exists, returns its info.
func (s *CollectionService) Ensure(
	ctx context.Context, name string, opts ...CollectionOption,
) (_ CollectionInfo, err error) {
	start := time.Now()
	defer func() { s.obs.observe("collection.ensure", start, err) }()

	cfg := &collectionConfig{colType: CollectionTypeText}
	for _, o := range opts {
		o.applyCollection(cfg)
	}

	fields, err := toInternalFields(cfg.fields)
	if err != nil {
		return CollectionInfo{}, fmt.Errorf("ensure collection: %w", err)
	}

	col, err := s.svc.Create(ctx, name, domcol.Type(cfg.colType), fields)
	if err == nil {
		return fromInternalCollection(col), nil
	}
	if !errors.Is(err, domain.ErrAlreadyExists) {
		return CollectionInfo{}, fmt.Errorf("ensure collection: %w", err)
	}

	existing, err := s.svc.Get(ctx, name)
	if err != nil {
		return CollectionInfo{}, fmt.Errorf("ensure collection: %w", err)
	}
	return fromInternalCollection(existing), nil
}

// Get retrieves collection metadata by name.
func (s *CollectionService) Get(
	ctx context.Context, name string,
) (_ CollectionInfo, err error) {
	start := time.Now()
	defer func() { s.obs.observe("collection.get", start, err) }()

	col, err := s.svc.Get(ctx, name)
	if err != nil {
		return CollectionInfo{}, fmt.Errorf("get collection: %w", err)
	}
	return fromInternalCollection(col), nil
}

// List returns a paginated list of collections.
// Cursor is a collection name to start after (empty for first page).
// If cursor references a deleted or non-existent collection, an empty page is
// returned (HasMore=false). Callers should treat this as end-of-list.
// Limit controls page size (0 = return all).
func (s *CollectionService) List(
	ctx context.Context, cursor string, limit int,
) (_ CollectionListResult, err error) {
	start := time.Now()
	defer func() { s.obs.observe("collection.list", start, err) }()

	cols, err := s.svc.List(ctx)
	if err != nil {
		return CollectionListResult{}, fmt.Errorf("list collections: %w", err)
	}

	all := make([]CollectionInfo, len(cols))
	for i, c := range cols {
		all[i] = fromInternalCollection(c)
	}

	// Client-side cursor pagination (collections are small, tens at most).
	if cursor != "" {
		startIdx := -1
		for i, c := range all {
			if c.Name == cursor {
				startIdx = i
				break
			}
		}
		if startIdx >= 0 && startIdx+1 < len(all) {
			all = all[startIdx+1:]
		} else {
			all = nil
		}
	}

	if limit <= 0 || limit >= len(all) {
		return CollectionListResult{Collections: all}, nil
	}

	page := all[:limit]
	return CollectionListResult{
		Collections: page,
		NextCursor:  page[len(page)-1].Name,
		HasMore:     true,
	}, nil
}

// Delete removes a collection.
func (s *CollectionService) Delete(
	ctx context.Context, name string,
) (err error) {
	start := time.Now()
	defer func() { s.obs.observe("collection.delete", start, err) }()

	if err = s.svc.Delete(ctx, name); err != nil {
		return fmt.Errorf("delete collection: %w", err)
	}
	return nil
}

func toInternalFields(fields []FieldInfo) ([]field.Field, error) {
	out := make([]field.Field, len(fields))
	for i, f := range fields {
		var err error
		out[i], err = field.New(f.Name, field.Type(f.Type))
		if err != nil {
			return nil, fmt.Errorf("field %q: %w", f.Name, err)
		}
	}
	return out, nil
}

func fromInternalCollection(col domcol.Collection) CollectionInfo {
	fields := make([]FieldInfo, len(col.Fields()))
	for i, f := range col.Fields() {
		fields[i] = FieldInfo{Name: f.Name(), Type: FieldType(f.FieldType())}
	}
	return CollectionInfo{
		Name:      col.Name(),
		Type:      CollectionType(col.Type()),
		Fields:    fields,
		VectorDim: col.VectorDim(),
		Revision:  col.Revision(),
		CreatedAt: col.CreatedAt(),
	}
}
