package request

import (
	"fmt"
	"math"
	"unicode/utf8"

	"github.com/kailas-cloud/vecdex/internal/domain"
	"github.com/kailas-cloud/vecdex/internal/domain/search/filter"
	"github.com/kailas-cloud/vecdex/internal/domain/search/mode"
)

// Search parameter limits shared by HTTP and embedded callers.
const (
	MaxQueryLength         = 4096
	DefaultLimit           = 10
	MaxLimit               = 100
	DefaultMaxContentBytes = 16 * 1024
	MaxContentBytes        = 160 * 1024
)

// Request is a validated source-document search with one result limit.
type Request struct {
	query           string
	searchMode      mode.Mode
	filters         filter.Expression
	limit           int
	maxContentBytes int
	minScore        float64
	includeVectors  bool
}

// New validates a search. Zero-valued optional limits use the documented defaults.
// Candidate expansion for fragments is an internal retrieval concern.
func New(
	query string, m mode.Mode, filters filter.Expression,
	limit, maxContentBytes int, minScore float64, includeVectors bool,
) (Request, error) {
	if !utf8.ValidString(query) || query == "" || utf8.RuneCountInString(query) > MaxQueryLength {
		return Request{}, fmt.Errorf("query must contain 1–%d characters: %w", MaxQueryLength, domain.ErrInvalidSchema)
	}
	if m == "" {
		m = mode.Hybrid
	}
	if !m.IsValid() {
		return Request{}, fmt.Errorf("invalid search mode %q: %w", m, domain.ErrInvalidSchema)
	}
	limit, maxContentBytes, err := normalizeLimits(limit, maxContentBytes)
	if err != nil {
		return Request{}, err
	}
	if math.IsNaN(minScore) || math.IsInf(minScore, 0) || minScore < 0 || minScore > 1 {
		return Request{}, fmt.Errorf("min_score must be between 0 and 1: %w", domain.ErrInvalidSchema)
	}
	return Request{query: query, searchMode: m, filters: filters, limit: limit,
		maxContentBytes: maxContentBytes, minScore: minScore, includeVectors: includeVectors}, nil
}

func normalizeLimits(limit, contentBytes int) (normalizedLimit, normalizedContentBytes int, err error) {
	if limit == 0 {
		limit = DefaultLimit
	}
	if contentBytes == 0 {
		contentBytes = DefaultMaxContentBytes
	}
	if limit < 1 || limit > MaxLimit {
		return 0, 0, fmt.Errorf("limit must be between 1 and %d: %w", MaxLimit, domain.ErrInvalidSchema)
	}
	if contentBytes < 1 || contentBytes > MaxContentBytes {
		return 0, 0, fmt.Errorf("max_content_bytes must be between 1 and %d: %w", MaxContentBytes, domain.ErrInvalidSchema)
	}
	return limit, contentBytes, nil
}

// Query returns the query text.
func (r *Request) Query() string { return r.query }

// Mode returns the search strategy.
func (r *Request) Mode() mode.Mode { return r.searchMode }

// Filters returns the indexed-field pre-filter.
func (r *Request) Filters() filter.Expression { return r.filters }

// Limit returns the maximum number of source documents.
func (r *Request) Limit() int { return r.limit }

// MaxContentBytes bounds the combined UTF-8 snippet bytes in one response.
func (r *Request) MaxContentBytes() int { return r.maxContentBytes }

// MinScore returns the optional relevance threshold.
func (r *Request) MinScore() float64 { return r.minScore }

// IncludeVectors reports whether diagnostic vectors were requested.
func (r *Request) IncludeVectors() bool { return r.includeVectors }
