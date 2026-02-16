package request

import (
	"fmt"

	"github.com/kailas-cloud/vecdex/internal/domain/search/filter"
	"github.com/kailas-cloud/vecdex/internal/domain/search/mode"
)

// Search parameter limits.
const (
	// MaxQueryLength is the maximum allowed search query length.
	MaxQueryLength = 4096
	DefaultTopK    = 10
	MaxTopK        = 500
	DefaultLimit   = 20
	MaxLimit       = 100
)

// GeoQuery holds parsed latitude/longitude for geo search.
type GeoQuery struct {
	Latitude  float64
	Longitude float64
}

// Request is a validated search query.
type Request struct {
	query          string
	searchMode     mode.Mode
	filters        filter.Expression
	topK           int
	limit          int
	minScore       float64
	includeVectors bool
	geoQuery       *GeoQuery
}

// New validates and normalizes search parameters.
// Defaults: mode=hybrid, topK=10, limit=20. Limit is clamped to topK.
func New(
	query string,
	m mode.Mode,
	filters filter.Expression,
	topK, limit int,
	minScore float64,
	includeVectors bool,
	geoQuery *GeoQuery,
) (Request, error) {
	if query == "" {
		return Request{}, fmt.Errorf("query is required")
	}
	if len(query) > MaxQueryLength {
		return Request{}, fmt.Errorf("query too long (max %d chars)", MaxQueryLength)
	}
	if m == "" {
		m = mode.Hybrid
	}
	if !m.IsValid() {
		return Request{}, fmt.Errorf("invalid search mode: %q", m)
	}
	if topK <= 0 {
		topK = DefaultTopK
	}
	if topK > MaxTopK {
		topK = MaxTopK
	}
	if limit <= 0 {
		limit = DefaultLimit
	}
	if limit > MaxLimit {
		limit = MaxLimit
	}
	if limit > topK {
		limit = topK
	}
	// min_score 0-1 validation only for non-geo modes (geo scores are meters)
	if m != mode.Geo && (minScore < 0 || minScore > 1) {
		return Request{}, fmt.Errorf("min_score must be between 0 and 1")
	}

	return Request{
		query:          query,
		searchMode:     m,
		filters:        filters,
		topK:           topK,
		limit:          limit,
		minScore:       minScore,
		includeVectors: includeVectors,
		geoQuery:       geoQuery,
	}, nil
}

// Query returns the search query text.
func (r *Request) Query() string { return r.query }

// Mode returns the search strategy.
func (r *Request) Mode() mode.Mode { return r.searchMode }

// Filters returns the pre-filter expression.
func (r *Request) Filters() filter.Expression { return r.filters }

// TopK returns the number of KNN candidates to retrieve.
func (r *Request) TopK() int { return r.topK }

// Limit returns the maximum results to return.
func (r *Request) Limit() int { return r.limit }

// MinScore returns the minimum similarity threshold.
func (r *Request) MinScore() float64 { return r.minScore }

// IncludeVectors reports whether vectors should be included in results.
func (r *Request) IncludeVectors() bool { return r.includeVectors }

// GeoQuery returns the parsed geo coordinates (nil for non-geo modes).
func (r *Request) GeoQuery() *GeoQuery { return r.geoQuery }
