package filter

import "fmt"

// MaxConditionsPerGroup is the maximum number of conditions per filter group.
const MaxConditionsPerGroup = 32

// Expression is a structured filter with must/should/must_not boolean semantics.
type Expression struct {
	must    []Condition
	should  []Condition
	mustNot []Condition
}

// NewExpression validates and creates a filter Expression.
func NewExpression(must, should, mustNot []Condition) (Expression, error) {
	if len(must) > MaxConditionsPerGroup {
		return Expression{}, fmt.Errorf("too many must conditions (max %d)", MaxConditionsPerGroup)
	}
	if len(should) > MaxConditionsPerGroup {
		return Expression{}, fmt.Errorf("too many should conditions (max %d)", MaxConditionsPerGroup)
	}
	if len(mustNot) > MaxConditionsPerGroup {
		return Expression{}, fmt.Errorf("too many must_not conditions (max %d)", MaxConditionsPerGroup)
	}
	return Expression{must: must, should: should, mustNot: mustNot}, nil
}

// Must returns the must conditions.
func (e Expression) Must() []Condition { return e.must }

// Should returns the should conditions.
func (e Expression) Should() []Condition { return e.should }

// MustNot returns the must-not conditions.
func (e Expression) MustNot() []Condition { return e.mustNot }

// IsEmpty reports whether the expression has no conditions.
func (e Expression) IsEmpty() bool {
	return len(e.must) == 0 && len(e.should) == 0 && len(e.mustNot) == 0
}

// Condition is a single filter clause: either a tag match or a numeric range.
type Condition struct {
	key       string
	match     string
	rangeExpr *Range
}

// NewMatch creates an exact tag match condition.
func NewMatch(key, match string) (Condition, error) {
	if key == "" {
		return Condition{}, fmt.Errorf("filter key is required")
	}
	if match == "" {
		return Condition{}, fmt.Errorf("match value is required for key %q", key)
	}
	return Condition{key: key, match: match}, nil
}

// NewRange creates a numeric range condition.
func NewRange(key string, r Range) (Condition, error) {
	if key == "" {
		return Condition{}, fmt.Errorf("filter key is required")
	}
	return Condition{key: key, rangeExpr: &r}, nil
}

// Key returns the field name.
func (c Condition) Key() string { return c.key }

// Match returns the exact match value.
func (c Condition) Match() string { return c.match }

// Range returns the numeric range expression.
func (c Condition) Range() *Range { return c.rangeExpr }

// IsMatch reports whether this is a match condition.
func (c Condition) IsMatch() bool { return c.match != "" }

// IsRange reports whether this is a range condition.
func (c Condition) IsRange() bool { return c.rangeExpr != nil }

// Range is a numeric range with gt/gte/lt/lte boundaries.
type Range struct {
	gt  *float64
	gte *float64
	lt  *float64
	lte *float64
}

// NewRangeFilter validates and creates a Range.
// At least one boundary required. gt/gte and lt/lte are mutually exclusive.
func NewRangeFilter(gt, gte, lt, lte *float64) (Range, error) {
	if gt == nil && gte == nil && lt == nil && lte == nil {
		return Range{}, fmt.Errorf("at least one range boundary is required")
	}
	if gt != nil && gte != nil {
		return Range{}, fmt.Errorf("cannot specify both gt and gte")
	}
	if lt != nil && lte != nil {
		return Range{}, fmt.Errorf("cannot specify both lt and lte")
	}
	return Range{gt: gt, gte: gte, lt: lt, lte: lte}, nil
}

// GT returns the lower exclusive bound.
func (r Range) GT() *float64 { return r.gt }

// GTE returns the lower inclusive bound.
func (r Range) GTE() *float64 { return r.gte }

// LT returns the upper exclusive bound.
func (r Range) LT() *float64 { return r.lt }

// LTE returns the upper inclusive bound.
func (r Range) LTE() *float64 { return r.lte }
