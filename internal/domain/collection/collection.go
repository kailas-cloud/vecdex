package collection

import (
	"fmt"
	"regexp"
	"time"

	"github.com/kailas-cloud/vecdex/internal/domain/collection/field"
)

var nameRegex = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

// Collection is the document collection aggregate (immutable value object).
type Collection struct {
	name      string
	fields    []field.Field
	vectorDim int
	createdAt int64
	revision  int
}

// New validates and creates a Collection.
// Name: ^[a-zA-Z0-9_-]+$, 1-64 chars. Fields: unique names, max 64. VectorDim: > 0.
func New(name string, fields []field.Field, vectorDim int) (Collection, error) {
	if name == "" {
		return Collection{}, fmt.Errorf("collection name is required")
	}
	if len(name) > 64 {
		return Collection{}, fmt.Errorf("collection name too long (max 64)")
	}
	if !nameRegex.MatchString(name) {
		return Collection{}, fmt.Errorf("collection name must be alphanumeric with underscores and hyphens")
	}
	if vectorDim <= 0 {
		return Collection{}, fmt.Errorf("vector dimension must be positive")
	}
	if len(fields) > 64 {
		return Collection{}, fmt.Errorf("too many fields (max 64)")
	}

	seen := make(map[string]bool, len(fields))
	for _, f := range fields {
		if seen[f.Name()] {
			return Collection{}, fmt.Errorf("duplicate field name: %s", f.Name())
		}
		seen[f.Name()] = true
	}

	return Collection{
		name:      name,
		fields:    fields,
		vectorDim: vectorDim,
		createdAt: time.Now().UnixMilli(),
		revision:  1,
	}, nil
}

// Reconstruct creates a Collection without validation (storage hydration).
func Reconstruct(name string, fields []field.Field, vectorDim int, createdAt int64, revision int) Collection {
	return Collection{
		name:      name,
		fields:    fields,
		vectorDim: vectorDim,
		createdAt: createdAt,
		revision:  revision,
	}
}

// Name returns the collection name.
func (c Collection) Name() string { return c.name }

// Fields returns the indexed field definitions.
func (c Collection) Fields() []field.Field { return c.fields }

// VectorDim returns the vector dimension.
func (c Collection) VectorDim() int { return c.vectorDim }

// CreatedAt returns the creation timestamp (unix millis).
func (c Collection) CreatedAt() int64 { return c.createdAt }

// Revision returns the optimistic concurrency version.
func (c Collection) Revision() int { return c.revision }

// HasField checks if a field with the given name and type exists.
func (c Collection) HasField(name string, ft field.Type) bool {
	for _, f := range c.fields {
		if f.Name() == name && f.FieldType() == ft {
			return true
		}
	}
	return false
}

// FieldByName looks up a field by name.
func (c Collection) FieldByName(name string) (field.Field, bool) {
	for _, f := range c.fields {
		if f.Name() == name {
			return f, true
		}
	}
	return field.Field{}, false
}
