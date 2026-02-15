package usage

import (
	"github.com/kailas-cloud/vecdex/internal/domain/usage/budget"
	"github.com/kailas-cloud/vecdex/internal/domain/usage/metrics"
)

// Period is the aggregation granularity.
type Period string

// Aggregation period constants.
const (
	PeriodDay   Period = "day"
	PeriodMonth Period = "month"
	PeriodTotal Period = "total"
)

// Report is an embedding API usage report for a time period.
type Report struct {
	period      Period
	periodStart int64
	periodEnd   int64
	collection  string
	metrics     metrics.Metrics
	budget      budget.Budget
}

// NewReport creates a usage report.
func NewReport(period Period, start, end int64, col string, m metrics.Metrics, b budget.Budget) Report {
	return Report{
		period:      period,
		periodStart: start,
		periodEnd:   end,
		collection:  col,
		metrics:     m,
		budget:      b,
	}
}

// Period returns the aggregation granularity.
func (r *Report) Period() Period { return r.period }

// PeriodStart returns the period start timestamp (unix millis).
func (r *Report) PeriodStart() int64 { return r.periodStart }

// PeriodEnd returns the period end timestamp (unix millis).
func (r *Report) PeriodEnd() int64 { return r.periodEnd }

// Collection returns the collection filter, if any.
func (r *Report) Collection() string { return r.collection }

// Metrics returns the usage metrics.
func (r *Report) Metrics() metrics.Metrics { return r.metrics }

// Budget returns the budget status.
func (r *Report) Budget() budget.Budget { return r.budget }
