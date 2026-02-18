package vecdex

// CollectionType distinguishes collection kinds.
type CollectionType string

// Collection type constants.
const (
	CollectionTypeText CollectionType = "text"
	CollectionTypeGeo  CollectionType = "geo"
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
	ModeGeo      SearchMode = "geo"
)

// CollectionInfo represents collection metadata.
type CollectionInfo struct {
	Name      string
	Type      CollectionType
	Fields    []FieldInfo
	VectorDim int
	CreatedAt int64
}

// FieldInfo represents a collection field definition.
type FieldInfo struct {
	Name string
	Type FieldType
}

// Document is an untyped document for the low-level API.
type Document struct {
	ID       string
	Content  string
	Tags     map[string]string
	Numerics map[string]float64
}

// DocumentPatch is a partial document update.
// Nil fields are unchanged. A nil value in Tags/Numerics means delete that key.
type DocumentPatch struct {
	Content  *string
	Tags     map[string]*string
	Numerics map[string]*float64
}

// SearchResult is a single search hit.
type SearchResult struct {
	ID       string
	Score    float64
	Content  string
	Tags     map[string]string
	Numerics map[string]float64
}

// BatchResult is the outcome of one item in a batch operation.
type BatchResult struct {
	ID  string
	OK  bool
	Err error
}

// ListResult is a paginated list of documents.
type ListResult struct {
	Documents  []Document
	NextCursor string
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
