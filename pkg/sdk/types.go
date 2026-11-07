package vecdex

// CollectionType distinguishes collection kinds.
type CollectionType string

// Collection type constants.
const (
	CollectionTypeText CollectionType = "text"
)

// FieldType defines the type of a collection field.
type FieldType string

// Field type constants.
const (
	FieldTag     FieldType = "tag"
	FieldNumeric FieldType = "numeric"
)

// SearchMode controls the search algorithm.
type SearchMode string

// Search mode constants.
const (
	ModeHybrid   SearchMode = "hybrid"
	ModeSemantic SearchMode = "semantic"
	ModeKeyword  SearchMode = "keyword"
)

// CollectionInfo represents collection metadata.
type CollectionInfo struct {
	Generation string
	Name       string
	Type       CollectionType
	Fields     []FieldInfo
	VectorDim  int
	Revision   int
	CreatedAt  int64
}

// CollectionInspection includes creating/deleting lifecycle states and absence.
type CollectionInspection struct {
	Name       string
	Generation string
	State      string
	Collection *CollectionInfo
}

// SourceProjection selects source text and metadata without fragment vectors.
type SourceProjection struct {
	Content  bool
	Metadata bool
}

// FieldInfo represents a collection field definition.
type FieldInfo struct {
	Name string
	Type FieldType
}

// Document is an untyped document for the low-level API.
type Document struct {
	// ExpiresAt is an absolute UTC Unix millisecond deadline; nil is persistent.
	ExpiresAt *int64
	Metadata  map[string]any
	// Version is an opaque token for IfVersion conditional writes.
	Version string
	ID      string
	Content string
}

// DocumentPatch is a partial document update.
// Nil fields are unchanged. Metadata follows JSON Merge Patch.
type DocumentPatch struct {
	// ExpiresAt leaves the deadline unchanged when nil; a pointer to zero clears it.
	ExpiresAt *int64
	// Metadata merges JSON members; a pointer to a nil map clears all metadata.
	Metadata *map[string]any
	Content  *string
}

// WriteResult identifies the exact committed source version. Get returns the
// current complete document; a receipt does not retain a historical source copy.
type WriteResult struct {
	ID      string
	Version string
	Created bool
}

// SearchResult is a single search hit.
type SearchResult struct {
	Version  string
	Metadata map[string]any
	ID       string
	Score    float64
	Snippet  string
	Vector   []float32 // nil when not requested (IncludeVectors=false)
}

// SearchResponse wraps search results with metadata.
type SearchResponse struct {
	Results []SearchResult
	Total   int // ranked source documents after score filtering
	Limit   int
}

// BatchResult is the outcome of one item in a batch operation.
type BatchResult struct {
	Version string // committed source version on a successful upsert
	ID      string
	OK      bool
	Err     error
}

// BatchUpsertItem writes one source. An empty IfVersion is unconditional.
type BatchUpsertItem struct {
	ExpiresAt      *int64
	IdempotencyKey string
	ID             string
	Content        string
	Metadata       map[string]any
	IfVersion      string
}

// BatchDeleteItem deletes one source. An empty IfVersion is unconditional.
type BatchDeleteItem struct {
	IdempotencyKey string
	ID             string
	IfVersion      string
}

// BatchResponse wraps batch results with summary counts.
type BatchResponse struct {
	Results   []BatchResult
	Succeeded int
	Failed    int
}

// ListResult is a paginated list of documents.
type ListResult struct {
	Documents  []Document
	NextCursor string
}

// CollectionListResult is a paginated list of collections.
type CollectionListResult struct {
	Collections []CollectionInfo
	NextCursor  string
	HasMore     bool
}

// FilterExpression is a set of must/should/must_not filter conditions.
type FilterExpression struct {
	Must    []FilterCondition
	Should  []FilterCondition
	MustNot []FilterCondition
}

// FilterCondition is a single filter clause.
type FilterCondition struct {
	Key   string
	Match string       // non-empty for tag match
	Range *RangeFilter // non-nil for numeric range
}

// RangeFilter defines numeric range boundaries.
type RangeFilter struct {
	GT  *float64
	GTE *float64
	LT  *float64
	LTE *float64
}
