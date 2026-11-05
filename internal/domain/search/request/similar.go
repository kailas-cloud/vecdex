package request

import (
	"fmt"
	"math"

	"github.com/kailas-cloud/vecdex/internal/domain"
	"github.com/kailas-cloud/vecdex/internal/domain/search/filter"
)

// SimilarRequest reuses a stored source vector with the same response limits.
type SimilarRequest struct {
	filters         filter.Expression
	limit           int
	maxContentBytes int
	minScore        float64
	includeVectors  bool
}

// NewSimilar validates a source-similarity query.
func NewSimilar(
	filters filter.Expression, limit, maxContentBytes int, minScore float64, includeVectors bool,
) (SimilarRequest, error) {
	limit, maxContentBytes, err := normalizeLimits(limit, maxContentBytes)
	if err != nil {
		return SimilarRequest{}, err
	}
	if math.IsNaN(minScore) || math.IsInf(minScore, 0) || minScore < 0 || minScore > 1 {
		return SimilarRequest{}, fmt.Errorf("min_score must be between 0 and 1: %w", domain.ErrInvalidSchema)
	}
	return SimilarRequest{filters: filters, limit: limit, maxContentBytes: maxContentBytes,
		minScore: minScore, includeVectors: includeVectors}, nil
}

// Filters returns the indexed-field pre-filter.
func (r *SimilarRequest) Filters() filter.Expression { return r.filters }

// Limit returns the source-document limit.
func (r *SimilarRequest) Limit() int { return r.limit }

// MaxContentBytes bounds combined response snippet bytes.
func (r *SimilarRequest) MaxContentBytes() int { return r.maxContentBytes }

// MinScore returns the optional similarity threshold.
func (r *SimilarRequest) MinScore() float64 { return r.minScore }

// IncludeVectors reports whether diagnostic vectors were requested.
func (r *SimilarRequest) IncludeVectors() bool { return r.includeVectors }
