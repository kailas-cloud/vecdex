package request

import (
	"errors"
	"math"
	"strings"
	"testing"

	"github.com/kailas-cloud/vecdex/internal/domain"
	"github.com/kailas-cloud/vecdex/internal/domain/search/filter"
	"github.com/kailas-cloud/vecdex/internal/domain/search/mode"
)

func TestSearchDefaultsHaveOneSourceLimitAndTextBudget(t *testing.T) {
	r, err := New("query", "", filter.Expression{}, 0, 0, 0, false)
	if err != nil || r.Mode() != mode.Hybrid || r.Limit() != 10 || r.MaxContentBytes() != 16384 {
		t.Fatalf("defaults: %+v %v", r, err)
	}
	if r.Query() != "query" || r.MinScore() != 0 || r.IncludeVectors() {
		t.Fatal("unexpected normalized search")
	}
}

func TestSearchExplicitLimitsAndFilters(t *testing.T) {
	r, err := New("query", mode.Semantic, filter.Expression{}, 50, 2048, .5, true)
	if err != nil || r.Limit() != 50 || r.MaxContentBytes() != 2048 || r.MinScore() != .5 || !r.IncludeVectors() {
		t.Fatalf("explicit search: %+v %v", r, err)
	}
}

func TestInvalidSearchParametersAreRejectedConsistently(t *testing.T) {
	cases := []struct {
		name, query string
		mode        mode.Mode
		limit, size int
		score       float64
	}{
		{name: "empty query"},
		{name: "long query", query: strings.Repeat("x", MaxQueryLength+1)},
		{name: "mode", query: "q", mode: "unknown"},
		{name: "negative limit", query: "q", limit: -1},
		{name: "large limit", query: "q", limit: 101},
		{name: "negative text budget", query: "q", size: -1},
		{name: "large text budget", query: "q", size: MaxContentBytes + 1},
		{name: "negative score", query: "q", score: -.1},
		{name: "large score", query: "q", score: 1.1},
		{name: "NaN score", query: "q", score: math.NaN()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := New(tc.query, tc.mode, filter.Expression{}, tc.limit, tc.size, tc.score, false)
			if !errors.Is(err, domain.ErrInvalidSchema) {
				t.Fatalf("invalid search accepted: %v", err)
			}
		})
	}
}

func TestSearchQueryLimitCountsUnicodeCharacters(t *testing.T) {
	for _, m := range []mode.Mode{mode.Hybrid, mode.Semantic, mode.Keyword} {
		if _, err := New(strings.Repeat("界", MaxQueryLength), m,
			filter.Expression{}, MaxLimit, MaxContentBytes, 0, false); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSimilarUsesTheSameLimits(t *testing.T) {
	r, err := NewSimilar(filter.Expression{}, 0, 0, .5, true)
	if err != nil || r.Limit() != DefaultLimit || r.MaxContentBytes() != DefaultMaxContentBytes ||
		r.MinScore() != .5 || !r.IncludeVectors() {
		t.Fatalf("similar defaults: %+v %v", r, err)
	}
	for _, limit := range []int{-1, MaxLimit + 1} {
		if _, err := NewSimilar(filter.Expression{}, limit, 0, 0, false); !errors.Is(err, domain.ErrInvalidSchema) {
			t.Fatalf("invalid similar limit accepted: %v", err)
		}
	}
	if _, err := NewSimilar(filter.Expression{}, 10, 0, math.NaN(), false); err == nil {
		t.Fatal("NaN similarity threshold accepted")
	}
}
