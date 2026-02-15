package domain

// Document is a document in a collection (legacy flat struct, see document/ for the aggregate).
type Document struct {
	ID       string
	Content  string
	Tags     map[string]string
	Numerics map[string]float64
	Vector   []float32 // not exposed to clients
}

// SearchResult is a single search hit.
type SearchResult struct {
	ID       string
	Score    float64
	Content  string
	Tags     map[string]string
	Numerics map[string]float64
}

// SearchRequest is a search query (legacy flat struct, see search/request/ for the aggregate).
type SearchRequest struct {
	Query   string
	Filters map[string]string // pre-filter on TAG fields
	K       int               // default 10, max 100
}
