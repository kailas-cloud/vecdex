package domain

// CollectionType is the storage type of a collection.
type CollectionType string

const (
	// CollectionTypeJSON stores documents as JSON.
	CollectionTypeJSON CollectionType = "json"
)

// FieldType is the indexing type of a field.
type FieldType string

const (
	// FieldTag is a tag field type.
	FieldTag FieldType = "tag"
	// FieldNumeric is a numeric field type.
	FieldNumeric FieldType = "numeric"
)

// Field describes an indexed field in a collection.
type Field struct {
	Name string    `json:"name"`
	Type FieldType `json:"type"`
}

// Collection is a document collection (legacy flat struct, see collection/ for the aggregate).
type Collection struct {
	Name      string         `json:"name"`
	Type      CollectionType `json:"type"`
	Fields    []Field        `json:"fields,omitempty"`
	VectorDim int            `json:"vector_dim"`
	CreatedAt int64          `json:"created_at"` // unix millis
}
