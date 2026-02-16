package db

import "github.com/kailas-cloud/vecdex/internal/domain/search/filter"

// KNNQuery is the input for vector similarity search.
type KNNQuery struct {
	IndexName     string
	Filters       filter.Expression
	Vector        []float32
	K             int
	ReturnFields  []string
	IncludeVector bool
	RawScores     bool // return __vector_score as-is (for L2 distance in geo search)
}

// TextQuery is the input for BM25 text search.
type TextQuery struct {
	IndexName    string
	Query        string
	Filters      filter.Expression
	TopK         int
	ReturnFields []string
}

// SearchResult is the output of a search operation.
type SearchResult struct {
	Total   int
	Entries []SearchEntry
}

// SearchEntry is a single document hit from a search.
type SearchEntry struct {
	Key    string
	Score  float64
	Fields map[string]string
}
