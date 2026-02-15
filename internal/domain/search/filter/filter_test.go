package filter

import (
	"strings"
	"testing"
)

func floatPtr(f float64) *float64 { return &f }

// --- Range tests ---

func TestNewRangeFilter_Valid(t *testing.T) {
	tests := []struct {
		name             string
		gt, gte, lt, lte *float64
	}{
		{"gt only", floatPtr(1), nil, nil, nil},
		{"gte only", nil, floatPtr(0), nil, nil},
		{"lt only", nil, nil, floatPtr(10), nil},
		{"lte only", nil, nil, nil, floatPtr(100)},
		{"gt+lt", floatPtr(0), nil, floatPtr(10), nil},
		{"gte+lte", nil, floatPtr(0), nil, floatPtr(10)},
		{"gt+lte", floatPtr(0), nil, nil, floatPtr(10)},
		{"gte+lt", nil, floatPtr(0), floatPtr(10), nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, err := NewRangeFilter(tt.gt, tt.gte, tt.lt, tt.lte)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if (r.GT() == nil) != (tt.gt == nil) {
				t.Error("GT() mismatch")
			}
			if (r.GTE() == nil) != (tt.gte == nil) {
				t.Error("GTE() mismatch")
			}
			if (r.LT() == nil) != (tt.lt == nil) {
				t.Error("LT() mismatch")
			}
			if (r.LTE() == nil) != (tt.lte == nil) {
				t.Error("LTE() mismatch")
			}
		})
	}
}

func TestNewRangeFilter_NoBoundary(t *testing.T) {
	_, err := NewRangeFilter(nil, nil, nil, nil)
	if err == nil {
		t.Fatal("expected error for no boundary")
	}
	if !strings.Contains(err.Error(), "at least one") {
		t.Errorf("error = %q", err)
	}
}

func TestNewRangeFilter_BothGtAndGte(t *testing.T) {
	_, err := NewRangeFilter(floatPtr(1), floatPtr(1), nil, nil)
	if err == nil {
		t.Fatal("expected error for both gt and gte")
	}
	if !strings.Contains(err.Error(), "gt and gte") {
		t.Errorf("error = %q", err)
	}
}

func TestNewRangeFilter_BothLtAndLte(t *testing.T) {
	_, err := NewRangeFilter(nil, nil, floatPtr(1), floatPtr(1))
	if err == nil {
		t.Fatal("expected error for both lt and lte")
	}
	if !strings.Contains(err.Error(), "lt and lte") {
		t.Errorf("error = %q", err)
	}
}

// --- Condition tests ---

func TestNewMatch_Valid(t *testing.T) {
	c, err := NewMatch("language", "go")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.Key() != "language" {
		t.Errorf("Key() = %q", c.Key())
	}
	if c.Match() != "go" {
		t.Errorf("Match() = %q", c.Match())
	}
	if !c.IsMatch() {
		t.Error("IsMatch() = false")
	}
	if c.IsRange() {
		t.Error("IsRange() = true for match condition")
	}
	if c.Range() != nil {
		t.Error("Range() should be nil for match")
	}
}

func TestNewMatch_EmptyKey(t *testing.T) {
	_, err := NewMatch("", "go")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "key is required") {
		t.Errorf("error = %q", err)
	}
}

func TestNewMatch_EmptyValue(t *testing.T) {
	_, err := NewMatch("language", "")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "match value") {
		t.Errorf("error = %q", err)
	}
}

func TestNewRange_Valid(t *testing.T) {
	r, _ := NewRangeFilter(floatPtr(0), nil, floatPtr(100), nil)
	c, err := NewRange("priority", r)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.Key() != "priority" {
		t.Errorf("Key() = %q", c.Key())
	}
	if !c.IsRange() {
		t.Error("IsRange() = false")
	}
	if c.IsMatch() {
		t.Error("IsMatch() = true for range condition")
	}
	if c.Match() != "" {
		t.Error("Match() should be empty for range")
	}
	if c.Range() == nil {
		t.Fatal("Range() should not be nil")
	}
}

func TestNewRange_EmptyKey(t *testing.T) {
	r, _ := NewRangeFilter(floatPtr(0), nil, nil, nil)
	_, err := NewRange("", r)
	if err == nil {
		t.Fatal("expected error")
	}
}

// --- Expression tests ---

func TestNewExpression_Valid(t *testing.T) {
	m, _ := NewMatch("lang", "go")
	expr, err := NewExpression([]Condition{m}, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(expr.Must()) != 1 {
		t.Errorf("Must() len = %d", len(expr.Must()))
	}
	if len(expr.Should()) != 0 {
		t.Errorf("Should() len = %d", len(expr.Should()))
	}
	if len(expr.MustNot()) != 0 {
		t.Errorf("MustNot() len = %d", len(expr.MustNot()))
	}
	if expr.IsEmpty() {
		t.Error("IsEmpty() = true for non-empty expression")
	}
}

func TestNewExpression_Empty(t *testing.T) {
	expr, err := NewExpression(nil, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !expr.IsEmpty() {
		t.Error("IsEmpty() = false for empty expression")
	}
}

func TestNewExpression_AllGroups(t *testing.T) {
	m1, _ := NewMatch("a", "1")
	m2, _ := NewMatch("b", "2")
	m3, _ := NewMatch("c", "3")

	expr, err := NewExpression([]Condition{m1}, []Condition{m2}, []Condition{m3})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(expr.Must()) != 1 || len(expr.Should()) != 1 || len(expr.MustNot()) != 1 {
		t.Error("expected 1 condition in each group")
	}
}

func TestNewExpression_TooManyMust(t *testing.T) {
	conds := make([]Condition, MaxConditionsPerGroup+1)
	for i := range conds {
		conds[i] = Condition{key: "k", match: "v"}
	}
	_, err := NewExpression(conds, nil, nil)
	if err == nil {
		t.Fatal("expected error for too many must conditions")
	}
	if !strings.Contains(err.Error(), "too many must") {
		t.Errorf("error = %q", err)
	}
}

func TestNewExpression_TooManyShould(t *testing.T) {
	conds := make([]Condition, MaxConditionsPerGroup+1)
	for i := range conds {
		conds[i] = Condition{key: "k", match: "v"}
	}
	_, err := NewExpression(nil, conds, nil)
	if err == nil {
		t.Fatal("expected error for too many should conditions")
	}
	if !strings.Contains(err.Error(), "too many should") {
		t.Errorf("error = %q", err)
	}
}

func TestNewExpression_TooManyMustNot(t *testing.T) {
	conds := make([]Condition, MaxConditionsPerGroup+1)
	for i := range conds {
		conds[i] = Condition{key: "k", match: "v"}
	}
	_, err := NewExpression(nil, nil, conds)
	if err == nil {
		t.Fatal("expected error for too many must_not conditions")
	}
	if !strings.Contains(err.Error(), "too many must_not") {
		t.Errorf("error = %q", err)
	}
}

func TestNewExpression_AtMaxConditions(t *testing.T) {
	conds := make([]Condition, MaxConditionsPerGroup)
	for i := range conds {
		conds[i] = Condition{key: "k", match: "v"}
	}
	_, err := NewExpression(conds, conds, conds)
	if err != nil {
		t.Fatalf("unexpected error for exactly max conditions: %v", err)
	}
}
