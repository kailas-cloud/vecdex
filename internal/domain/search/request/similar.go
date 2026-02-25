package request

import (
	"fmt"

	"github.com/kailas-cloud/vecdex/internal/domain/search/filter"
)

// SimilarRequest is a validated "find similar" query.
type SimilarRequest struct {
	filters        filter.Expression
	topK           int
	limit          int
	minScore       float64
	includeVectors bool
}

// NewSimilar validates and normalizes similar request parameters.
func NewSimilar(
	filters filter.Expression,
	topK, limit int,
	minScore float64,
	includeVectors bool,
) (SimilarRequest, error) {
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
	if minScore < 0 || minScore > 1 {
		return SimilarRequest{}, fmt.Errorf("min_score must be between 0 and 1")
	}

	return SimilarRequest{
		filters:        filters,
		topK:           topK,
		limit:          limit,
		minScore:       minScore,
		includeVectors: includeVectors,
	}, nil
}

// Filters returns the pre-filter expression.
func (r *SimilarRequest) Filters() filter.Expression { return r.filters }

// TopK returns the number of KNN candidates to retrieve.
func (r *SimilarRequest) TopK() int { return r.topK }

// Limit returns the maximum results to return.
func (r *SimilarRequest) Limit() int { return r.limit }

// MinScore returns the minimum similarity threshold.
func (r *SimilarRequest) MinScore() float64 { return r.minScore }

// IncludeVectors reports whether vectors should be included in results.
func (r *SimilarRequest) IncludeVectors() bool { return r.includeVectors }
