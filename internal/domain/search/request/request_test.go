package request

import (
	"strings"
	"testing"

	"github.com/kailas-cloud/vecdex/internal/domain/search/filter"
	"github.com/kailas-cloud/vecdex/internal/domain/search/mode"
)

func emptyFilters() filter.Expression {
	e, _ := filter.NewExpression(nil, nil, nil)
	return e
}

func TestNew_Defaults(t *testing.T) {
	r, err := New("hello", "", emptyFilters(), 0, 0, 0, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r.Query() != "hello" {
		t.Errorf("Query() = %q", r.Query())
	}
	if r.Mode() != mode.Hybrid {
		t.Errorf("Mode() = %q, want hybrid (default)", r.Mode())
	}
	if r.TopK() != DefaultTopK {
		t.Errorf("TopK() = %d, want %d", r.TopK(), DefaultTopK)
	}
	if r.Limit() != DefaultTopK {
		// limit(20) > topK(10) => limit = topK
		t.Errorf("Limit() = %d, want %d (clamped to topK)", r.Limit(), DefaultTopK)
	}
	if r.MinScore() != 0 {
		t.Errorf("MinScore() = %f", r.MinScore())
	}
	if r.IncludeVectors() {
		t.Error("IncludeVectors() = true")
	}
}

func TestNew_ExplicitValues(t *testing.T) {
	r, err := New("query", mode.Semantic, emptyFilters(), 50, 20, 0.5, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r.Mode() != mode.Semantic {
		t.Errorf("Mode() = %q", r.Mode())
	}
	if r.TopK() != 50 {
		t.Errorf("TopK() = %d", r.TopK())
	}
	if r.Limit() != 20 {
		t.Errorf("Limit() = %d", r.Limit())
	}
	if r.MinScore() != 0.5 {
		t.Errorf("MinScore() = %f", r.MinScore())
	}
	if !r.IncludeVectors() {
		t.Error("IncludeVectors() = false")
	}
}

func TestNew_EmptyQuery(t *testing.T) {
	_, err := New("", mode.Hybrid, emptyFilters(), 10, 10, 0, false)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "required") {
		t.Errorf("error = %q", err)
	}
}

func TestNew_QueryTooLong(t *testing.T) {
	_, err := New(strings.Repeat("x", MaxQueryLength+1), mode.Hybrid, emptyFilters(), 10, 10, 0, false)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "too long") {
		t.Errorf("error = %q", err)
	}
}

func TestNew_QueryAtMaxLength(t *testing.T) {
	_, err := New(strings.Repeat("x", MaxQueryLength), mode.Hybrid, emptyFilters(), 10, 10, 0, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestNew_InvalidMode(t *testing.T) {
	_, err := New("query", "invalid", emptyFilters(), 10, 10, 0, false)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "invalid search mode") {
		t.Errorf("error = %q", err)
	}
}

func TestNew_AllValidModes(t *testing.T) {
	for _, m := range []mode.Mode{mode.Hybrid, mode.Semantic, mode.Keyword} {
		_, err := New("q", m, emptyFilters(), 10, 10, 0, false)
		if err != nil {
			t.Errorf("unexpected error for mode %q: %v", m, err)
		}
	}
}

func TestNew_TopKClamping(t *testing.T) {
	tests := []struct {
		name     string
		topK     int
		wantTopK int
	}{
		{"negative", -1, DefaultTopK},
		{"zero", 0, DefaultTopK},
		{"normal", 100, 100},
		{"over max", 1000, MaxTopK},
		{"exactly max", MaxTopK, MaxTopK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, err := New("q", mode.Hybrid, emptyFilters(), tt.topK, 1, 0, false)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if r.TopK() != tt.wantTopK {
				t.Errorf("TopK() = %d, want %d", r.TopK(), tt.wantTopK)
			}
		})
	}
}

func TestNew_LimitClamping(t *testing.T) {
	tests := []struct {
		name      string
		topK      int
		limit     int
		wantLimit int
	}{
		{"negative limit", 100, -1, DefaultLimit},
		{"zero limit", 100, 0, DefaultLimit},
		{"normal", 100, 50, 50},
		{"over max", 100, 200, MaxLimit},
		{"limit > topK", 5, 10, 5},
		{"limit = topK", 20, 20, 20},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, err := New("q", mode.Hybrid, emptyFilters(), tt.topK, tt.limit, 0, false)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if r.Limit() != tt.wantLimit {
				t.Errorf("Limit() = %d, want %d", r.Limit(), tt.wantLimit)
			}
		})
	}
}

func TestNew_MinScoreValidation(t *testing.T) {
	// Valid values
	for _, s := range []float64{0, 0.5, 1} {
		_, err := New("q", mode.Hybrid, emptyFilters(), 10, 10, s, false)
		if err != nil {
			t.Errorf("unexpected error for min_score=%f: %v", s, err)
		}
	}

	// Invalid values
	for _, s := range []float64{-0.1, 1.1, -1, 2} {
		_, err := New("q", mode.Hybrid, emptyFilters(), 10, 10, s, false)
		if err == nil {
			t.Errorf("expected error for min_score=%f", s)
		}
	}
}

func TestNew_WithFilters(t *testing.T) {
	m, _ := filter.NewMatch("lang", "go")
	expr, _ := filter.NewExpression([]filter.Condition{m}, nil, nil)

	r, err := New("query", mode.Hybrid, expr, 10, 10, 0, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r.Filters().IsEmpty() {
		t.Error("Filters().IsEmpty() = true, want false")
	}
}
