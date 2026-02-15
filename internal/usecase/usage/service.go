package usage

import (
	"context"
	"time"

	domusage "github.com/kailas-cloud/vecdex/internal/domain/usage"
	"github.com/kailas-cloud/vecdex/internal/domain/usage/budget"
	"github.com/kailas-cloud/vecdex/internal/domain/usage/metrics"
)

// Service handles usage reporting.
type Service struct {
	br BudgetReader
}

// New creates a Service. br can be nil (unlimited mode).
func New(br BudgetReader) *Service {
	return &Service{br: br}
}

// GetReport builds a usage report for the given period.
func (s *Service) GetReport(_ context.Context, period domusage.Period) domusage.Report {
	now := time.Now().UTC()
	var start, end int64
	var limit, used, remaining int64

	switch period {
	case domusage.PeriodDay:
		dayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
		dayEnd := dayStart.Add(24 * time.Hour)
		start = dayStart.UnixMilli()
		end = dayEnd.UnixMilli()
		if s.br != nil {
			limit = s.br.DailyLimit()
			used = s.br.DailyUsed()
			remaining = s.br.RemainingDaily()
		}
	case domusage.PeriodMonth:
		monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
		monthEnd := monthStart.AddDate(0, 1, 0)
		start = monthStart.UnixMilli()
		end = monthEnd.UnixMilli()
		if s.br != nil {
			limit = s.br.MonthlyLimit()
			used = s.br.MonthlyUsed()
			remaining = s.br.RemainingMonthly()
		}
	default:
		// total — no period boundaries
		if s.br != nil {
			limit = s.br.MonthlyLimit()
			used = s.br.MonthlyUsed()
			remaining = s.br.RemainingMonthly()
		}
	}

	exhausted := limit > 0 && remaining <= 0
	resetsAt := end

	b := budget.New(int(limit), int(remaining), exhausted, resetsAt)
	m := metrics.New(0, int(used), 0) // requests and cost_millidollars not tracked per-period yet

	return domusage.NewReport(period, start, end, "", m, b)
}
