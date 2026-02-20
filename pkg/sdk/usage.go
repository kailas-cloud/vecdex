package vecdex

import (
	"context"
	"time"

	domusage "github.com/kailas-cloud/vecdex/internal/domain/usage"
)

// UsagePeriod is the aggregation granularity for usage reports.
type UsagePeriod string

// UsagePeriod constants.
const (
	PeriodDay   UsagePeriod = "day"
	PeriodMonth UsagePeriod = "month"
	PeriodTotal UsagePeriod = "total"
)

// UsageReport contains embedding usage statistics for a time period.
type UsageReport struct {
	Period      UsagePeriod
	PeriodStart time.Time
	PeriodEnd   time.Time
	Metrics     UsageMetrics
	Budget      BudgetStatus
}

// UsageMetrics tracks embedding resource consumption.
type UsageMetrics struct {
	EmbeddingRequests int
	Tokens            int
	CostMillidollars  int
}

// BudgetStatus tracks token quota state.
type BudgetStatus struct {
	TokensLimit     int
	TokensRemaining int
	IsExhausted     bool
	ResetsAt        time.Time
}

// Usage returns an embedding usage report for the given period.
// Observer always records success — the underlying use-case is in-memory
// and does not produce errors.
func (c *Client) Usage(ctx context.Context, period UsagePeriod) UsageReport {
	start := time.Now()
	defer func() { c.obs.observe("usage", start, nil) }()

	report := c.usageSvc.GetReport(ctx, domusage.Period(period))
	m := report.Metrics()
	b := report.Budget()

	return UsageReport{
		Period:      UsagePeriod(report.Period()),
		PeriodStart: time.UnixMilli(report.PeriodStart()).UTC(),
		PeriodEnd:   time.UnixMilli(report.PeriodEnd()).UTC(),
		Metrics: UsageMetrics{
			EmbeddingRequests: m.EmbeddingRequests(),
			Tokens:            m.Tokens(),
			CostMillidollars:  m.CostMillidollars(),
		},
		Budget: BudgetStatus{
			TokensLimit:     b.TokensLimit(),
			TokensRemaining: b.TokensRemaining(),
			IsExhausted:     b.IsExhausted(),
			ResetsAt:        time.UnixMilli(b.ResetsAt()).UTC(),
		},
	}
}

// usageUseCase is the internal interface for usage reports.
type usageUseCase interface {
	GetReport(ctx context.Context, period domusage.Period) domusage.Report
}
