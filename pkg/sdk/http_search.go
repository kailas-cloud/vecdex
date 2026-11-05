package vecdex

import (
	"context"
	"fmt"
	"math"
	"net/http"

	"github.com/kailas-cloud/vecdex/internal/domain/metadata"
	"github.com/kailas-cloud/vecdex/internal/domain/search/filter"
	"github.com/kailas-cloud/vecdex/internal/domain/search/request"
	"github.com/kailas-cloud/vecdex/internal/domain/search/result"
	gen "github.com/kailas-cloud/vecdex/internal/transport/generated"
)

type httpSearch struct{ *httpBackend }

func (s *httpSearch) Search(
	ctx context.Context, collection string, req *request.Request,
) ([]result.Result, int, error) {
	mode := gen.SearchRequestMode(req.Mode())
	limit, contentBytes, minScore, vectors := req.Limit(), req.MaxContentBytes(), req.MinScore(), req.IncludeVectors()
	body := gen.SearchRequest{Query: req.Query(), Mode: &mode, Filters: filtersToHTTP(req.Filters()),
		Limit: &limit, MaxContentBytes: &contentBytes, MinScore: &minScore, IncludeVectors: &vectors}
	var reply gen.SearchResultListResponse
	if _, err := s.call(ctx, http.MethodPost, collectionHTTPPath(collection)+"/search", body, &reply); err != nil {
		return nil, 0, err
	}
	if reply.Limit != limit || len(reply.Items) > limit || reply.Total < len(reply.Items) {
		return nil, 0, fmt.Errorf("HTTP search response has inconsistent limits or count")
	}
	out := make([]result.Result, len(reply.Items))
	remaining := contentBytes
	for i, item := range reply.Items {
		if item.Id == "" || item.Version == "" || math.IsNaN(item.Score) || math.IsInf(item.Score, 0) {
			return nil, 0, fmt.Errorf("HTTP search response omitted source identity or score")
		}
		remaining -= len(item.Snippet)
		if remaining < 0 {
			return nil, 0, fmt.Errorf("HTTP search response exceeded its text budget")
		}
		value, err := metadata.Parse(item.Metadata)
		if err != nil {
			return nil, 0, fmt.Errorf("HTTP search metadata: %w", err)
		}
		var vector []float32
		if item.Vector != nil {
			vector = *item.Vector
		}
		out[i] = result.New(item.Id, item.Score, item.Snippet, nil, nil, vector).WithSource(item.Version, value)
	}
	return out, reply.Total, nil
}

func filtersToHTTP(expr filter.Expression) *gen.FilterExpression {
	if expr.IsEmpty() {
		return nil
	}
	return &gen.FilterExpression{
		Must: conditionsToHTTP(expr.Must()), Should: conditionsToHTTP(expr.Should()),
		MustNot: conditionsToHTTP(expr.MustNot()),
	}
}

func conditionsToHTTP(conditions []filter.Condition) *[]gen.FilterCondition {
	if len(conditions) == 0 {
		return nil
	}
	out := make([]gen.FilterCondition, len(conditions))
	for i, item := range conditions {
		out[i].Key = item.Key()
		if item.IsMatch() {
			value := item.Match()
			out[i].Match = &value
		} else if bounds := item.Range(); bounds != nil {
			out[i].Range = &gen.RangeFilter{Gt: bounds.GT(), Gte: bounds.GTE(), Lt: bounds.LT(), Lte: bounds.LTE()}
		}
	}
	return &out
}
