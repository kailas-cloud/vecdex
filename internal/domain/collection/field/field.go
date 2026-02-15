package field

import "fmt"

// Type is the indexing type of a field.
type Type string

// Field type constants.
const (
	// Tag is a tag (exact match) field.
	Tag     Type = "tag"
	Numeric Type = "numeric"
)

var reservedFieldNames = map[string]bool{
	"id": true, "content": true, "score": true, "vector": true,
}

// Field is an immutable value object describing an indexed collection field.
type Field struct {
	name      string
	fieldType Type
}

// New validates and creates a Field.
// Name must be non-empty, max 64 chars, and not reserved.
// Type must be tag or numeric.
func New(name string, ft Type) (Field, error) {
	if name == "" {
		return Field{}, fmt.Errorf("field name is required")
	}
	if len(name) > 64 {
		return Field{}, fmt.Errorf("field name %q too long (max 64)", name)
	}
	if reservedFieldNames[name] {
		return Field{}, fmt.Errorf("field name %q is reserved", name)
	}
	if ft != Tag && ft != Numeric {
		return Field{}, fmt.Errorf("invalid field type %q for %q", ft, name)
	}
	return Field{name: name, fieldType: ft}, nil
}

// Reconstruct creates a Field without validation (storage hydration).
func Reconstruct(name string, ft Type) Field {
	return Field{name: name, fieldType: ft}
}

// Name returns the field name.

// Name returns the field name.
// FieldType returns the field indexing type.
func (f Field) Name() string { return f.name }

// FieldType returns the field's indexing type.
func (f Field) FieldType() Type { return f.fieldType }
