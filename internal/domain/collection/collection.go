package collection

import (
	"crypto/rand"
	"fmt"
	"regexp"
	"time"

	"github.com/kailas-cloud/vecdex/internal/domain"
	"github.com/kailas-cloud/vecdex/internal/domain/collection/field"
)

var nameRegex = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

// Type distinguishes supported collection kinds.
type Type string

const (
	// TypeText is the default collection type with embedding-based vector search.
	TypeText Type = "text"
	// SystemParentDocID is an implicit tag field available in every collection.
	SystemParentDocID = "parent_doc_id"
	// SystemChunkIndex is an implicit numeric field available in every collection.
	SystemChunkIndex = "chunk_index"
)

var systemFields = []field.Field{
	field.Reconstruct(SystemParentDocID, field.Tag),
	field.Reconstruct(SystemChunkIndex, field.Numeric),
}

// IsValid checks if the collection type is supported.
func (t Type) IsValid() bool {
	return t == TypeText
}

// Collection is the document collection aggregate (immutable value object).
type Collection struct {
	name           string
	identity       *collectionIdentity
	collectionType Type
	fields         []field.Field
	vectorDim      int
	createdAt      int64
	revision       int
}

func validateName(name string) error {
	if name == "" {
		return fmt.Errorf("collection name is required")
	}
	if len(name) > 64 {
		return fmt.Errorf("collection name too long (max 64)")
	}
	if !nameRegex.MatchString(name) {
		return fmt.Errorf("collection name must be alphanumeric with underscores and hyphens")
	}
	return nil
}

func validateFields(fields []field.Field) error {
	if len(fields) > 64 {
		return fmt.Errorf("too many fields (max 64)")
	}
	seen := make(map[string]bool, len(fields))
	for _, f := range fields {
		if IsSystemField(f.Name()) {
			return fmt.Errorf("field name %q is reserved for system use", f.Name())
		}
		if seen[f.Name()] {
			return fmt.Errorf("duplicate field name: %s", f.Name())
		}
		seen[f.Name()] = true
	}
	return nil
}

// New validates and creates a Collection.
// Name: ^[a-zA-Z0-9_-]+$, 1-64 chars. Fields: unique names, max 64. VectorDim: > 0.
func New(name string, colType Type, fields []field.Field, vectorDim int) (Collection, error) {
	if colType == "" {
		colType = TypeText
	}
	if !colType.IsValid() {
		return Collection{}, fmt.Errorf("invalid collection type: %q", colType)
	}
	if err := validateName(name); err != nil {
		return Collection{}, err
	}
	if vectorDim <= 0 {
		return Collection{}, fmt.Errorf("vector dimension must be positive")
	}
	if err := validateFields(fields); err != nil {
		return Collection{}, err
	}

	return Collection{
		name:           name,
		collectionType: colType,
		fields:         fields,
		vectorDim:      vectorDim,
		createdAt:      time.Now().UnixMilli(),
		identity:       &collectionIdentity{generation: rand.Text()},
		revision:       1,
	}, nil
}

// Reconstruct creates a Collection without validation (storage hydration).
func Reconstruct(
	name string, colType Type, fields []field.Field,
	vectorDim int, createdAt int64, revision int,
) Collection {
	if colType == "" {
		colType = TypeText
	}
	return Collection{
		name:           name,
		collectionType: colType,
		fields:         fields,
		vectorDim:      vectorDim,
		createdAt:      createdAt,
		revision:       revision,
	}
}

// Name returns the collection name.
func (c Collection) Name() string { return c.name }

// Type returns the collection type.
func (c Collection) Type() Type { return c.collectionType }

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
	if sys, ok := SystemFieldByName(name); ok {
		return sys.FieldType() == ft
	}
	for _, f := range c.fields {
		if f.Name() == name && f.FieldType() == ft {
			return true
		}
	}
	return false
}

// FieldByName looks up a field by name.
func (c Collection) FieldByName(name string) (field.Field, bool) {
	if sys, ok := SystemFieldByName(name); ok {
		return sys, true
	}
	for _, f := range c.fields {
		if f.Name() == name {
			return f, true
		}
	}
	return field.Field{}, false
}

// SystemFields returns the implicit indexed fields available in every collection.
func SystemFields() []field.Field {
	out := make([]field.Field, len(systemFields))
	copy(out, systemFields)
	return out
}

// SystemFieldByName returns an implicit system field by name.
func SystemFieldByName(name string) (field.Field, bool) {
	for _, f := range systemFields {
		if f.Name() == name {
			return f, true
		}
	}
	return field.Field{}, false
}

// IsSystemField reports whether name is reserved for implicit collection fields.
func IsSystemField(name string) bool {
	_, ok := SystemFieldByName(name)
	return ok
}

type collectionIdentity struct{ generation, embeddingSpace string }

// Generation identifies this incarnation independently of its public name.
func (c Collection) Generation() string {
	if c.identity == nil {
		return ""
	}
	return c.identity.generation
}

// WithGeneration restores the persisted identity during hydration.
func (c Collection) WithGeneration(generation string) Collection {
	identity := collectionIdentity{generation: generation, embeddingSpace: c.EmbeddingSpace()}
	c.identity = &identity
	return c
}

// EmbeddingSpace identifies the vectorizer configuration used by this collection.
func (c Collection) EmbeddingSpace() string {
	if c.identity == nil {
		return ""
	}
	return c.identity.embeddingSpace
}

// WithEmbeddingSpace binds a new collection or restores its persisted vectorizer identity.
func (c Collection) WithEmbeddingSpace(fingerprint string) Collection {
	identity := collectionIdentity{generation: c.Generation(), embeddingSpace: fingerprint}
	c.identity = &identity
	return c
}

// CheckEmbeddingSpace prevents mixing incompatible vectors. An empty expected
// identity supports embedded clients that supply their own embedder.
func (c Collection) CheckEmbeddingSpace(expected string) error {
	if expected != "" && c.EmbeddingSpace() != expected {
		return domain.ErrEmbeddingSpaceMismatch
	}
	return nil
}
