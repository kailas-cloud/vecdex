package usage

import (
	"context"
	"fmt"
	"time"

	domusage "github.com/kailas-cloud/vecdex/internal/domain/usage"
	"github.com/kailas-cloud/vecdex/internal/domain/usage/budget"
	"github.com/kailas-cloud/vecdex/internal/domain/usage/metrics"
)

// Service handles usage reporting.
type Service struct{ br BudgetReader }

// New creates a Service. A nil reader represents an unused, unlimited embedder.
func New(br BudgetReader) *Service { return &Service{br: br} }

// GetReport uses measured tokens for usage and includes conservative reservations
// only in budget availability. Total usage is a lifetime counter, not a month alias.
func (s *Service) GetReport(ctx context.Context, period domusage.Period) (domusage.Report, error) {
	now := time.Now().UTC()
	var snapshot domusage.Snapshot
	if s.br != nil {
		var err error
		snapshot, err = s.br.Snapshot(ctx)
		if err != nil {
			return domusage.Report{}, fmt.Errorf("get usage report: %w", err)
		}
	}
	counters := snapshot.Total
	start, end := int64(0), int64(0)
	limit := int64(-1)
	budgetCounters := snapshot.Month
	if s.br != nil {
		limit = s.br.MonthlyLimit()
	}
	switch period {
	case domusage.PeriodDay:
		beginning := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
		start, end = beginning.UnixMilli(), beginning.AddDate(0, 0, 1).UnixMilli()
		counters = snapshot.Day
		budgetCounters = snapshot.Day
		if s.br != nil {
			limit = s.br.DailyLimit()
		}
	case domusage.PeriodMonth:
		beginning := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
		start, end = beginning.UnixMilli(), beginning.AddDate(0, 1, 0).UnixMilli()
		counters = snapshot.Month
	}
	remaining, resetsAt := int64(-1), int64(0)
	if limit > 0 {
		remaining = max(int64(0), limit-budgetCounters.Tokens-budgetCounters.Reserved)
		resetsAt = end
		if period == domusage.PeriodTotal {
			resetsAt = time.Date(now.Year(), now.Month()+1, 1, 0, 0, 0, 0, time.UTC).UnixMilli()
		}
	} else {
		limit = -1
	}
	b := budget.New(int(limit), int(remaining), limit > 0 && remaining == 0, resetsAt)
	m := metrics.New(int(counters.Requests), int(counters.Tokens), 0).WithUnknownRequests(int(counters.Unknown))
	return domusage.NewReport(period, start, end, "", m, b), nil
}
