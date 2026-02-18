package vecdex

import (
	"context"
	"fmt"

	"github.com/kailas-cloud/vecdex/internal/domain/search/filter"
	"github.com/kailas-cloud/vecdex/internal/domain/search/mode"
	"github.com/kailas-cloud/vecdex/internal/domain/search/request"
	"github.com/kailas-cloud/vecdex/internal/domain/search/result"
	searchuc "github.com/kailas-cloud/vecdex/internal/usecase/search"
)

// SearchService executes search queries against a single collection.
type SearchService struct {
	collection string
	svc        *searchuc.Service
}

// SearchOptions configures a search query.
type SearchOptions struct {
	Mode           SearchMode
	Filters        FilterExpression
	TopK           int
	Limit          int
	MinScore       float64
	IncludeVectors bool
}

// Query executes a text search (semantic, keyword, or hybrid).
func (s *SearchService) Query(
	ctx context.Context, query string, opts *SearchOptions,
) ([]SearchResult, error) {
	if opts == nil {
		opts = &SearchOptions{}
	}
	m := mode.Mode(opts.Mode)
	if m == "" {
		m = mode.Hybrid
	}

	filters, err := toInternalFilters(opts.Filters)
	if err != nil {
		return nil, fmt.Errorf("query: %w", err)
	}

	req, err := request.New(
		query, m, filters,
		opts.TopK, opts.Limit, opts.MinScore, opts.IncludeVectors, nil,
	)
	if err != nil {
		return nil, fmt.Errorf("query: %w", err)
	}

	results, err := s.svc.Search(ctx, s.collection, &req)
	if err != nil {
		return nil, fmt.Errorf("query: %w", err)
	}
	return fromSearchResults(results), nil
}

// Geo executes a geographic proximity search.
// Returns results sorted by distance (meters, ascending).
func (s *SearchService) Geo(
	ctx context.Context, lat, lon float64, topK int,
	opts ...SearchOptions,
) ([]SearchResult, error) {
	var so SearchOptions
	if len(opts) > 0 {
		so = opts[0]
	}

	filters, err := toInternalFilters(so.Filters)
	if err != nil {
		return nil, fmt.Errorf("geo search: %w", err)
	}

	geoQuery := &request.GeoQuery{Latitude: lat, Longitude: lon}
	req, err := request.New(
		"geo", mode.Geo, filters,
		topK, so.Limit, so.MinScore, so.IncludeVectors, geoQuery,
	)
	if err != nil {
		return nil, fmt.Errorf("geo search: %w", err)
	}

	results, err := s.svc.Search(ctx, s.collection, &req)
	if err != nil {
		return nil, fmt.Errorf("geo search: %w", err)
	}
	return fromSearchResults(results), nil
}

func toInternalFilters(fe FilterExpression) (filter.Expression, error) {
	must, err := toConditions(fe.Must)
	if err != nil {
		return filter.Expression{}, fmt.Errorf("filter must: %w", err)
	}
	should, err := toConditions(fe.Should)
	if err != nil {
		return filter.Expression{}, fmt.Errorf("filter should: %w", err)
	}
	mustNot, err := toConditions(fe.MustNot)
	if err != nil {
		return filter.Expression{}, fmt.Errorf("filter must_not: %w", err)
	}
	expr, err := filter.NewExpression(must, should, mustNot)
	if err != nil {
		return filter.Expression{}, fmt.Errorf("filter expression: %w", err)
	}
	return expr, nil
}

func toConditions(conds []FilterCondition) ([]filter.Condition, error) {
	if len(conds) == 0 {
		return nil, nil
	}
	out := make([]filter.Condition, len(conds))
	for i, c := range conds {
		var err error
		if c.Range != nil {
			r, rerr := filter.NewRangeFilter(
				c.Range.GT, c.Range.GTE, c.Range.LT, c.Range.LTE,
			)
			if rerr != nil {
				return nil, fmt.Errorf("filter %q: %w", c.Key, rerr)
			}
			out[i], err = filter.NewRange(c.Key, r)
		} else {
			out[i], err = filter.NewMatch(c.Key, c.Match)
		}
		if err != nil {
			return nil, fmt.Errorf("filter %q: %w", c.Key, err)
		}
	}
	return out, nil
}

func fromSearchResults(results []result.Result) []SearchResult {
	out := make([]SearchResult, len(results))
	for i := range results {
		r := &results[i]
		out[i] = SearchResult{
			ID:       r.ID(),
			Score:    r.Score(),
			Content:  r.Content(),
			Tags:     r.Tags(),
			Numerics: r.Numerics(),
		}
	}
	return out
}
