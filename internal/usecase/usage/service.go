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
	snapshot, err := s.readSnapshot(ctx)
	if err != nil {
		return domusage.Report{}, err
	}
	window := s.reportWindow(now, period, &snapshot)
	b := reportBudget(now, period, window)
	counters := window.counters
	m := metrics.New(int(counters.Requests), int(counters.Tokens), 0).WithUnknownRequests(int(counters.Unknown))
	return domusage.NewReport(period, window.start, window.end, "", m, b), nil
}

func (s *Service) readSnapshot(ctx context.Context) (domusage.Snapshot, error) {
	if s.br == nil {
		return domusage.Snapshot{}, nil
	}
	snapshot, err := s.br.Snapshot(ctx)
	if err != nil {
		return domusage.Snapshot{}, fmt.Errorf("get usage report: %w", err)
	}
	return snapshot, nil
}

type usageWindow struct {
	counters, budgetCounters domusage.Counters
	start, end, limit        int64
}

func (s *Service) reportWindow(now time.Time, period domusage.Period, snapshot *domusage.Snapshot) usageWindow {
	window := usageWindow{counters: snapshot.Total, budgetCounters: snapshot.Month, limit: s.periodLimit(period)}
	switch period {
	case domusage.PeriodDay:
		beginning := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
		window.start, window.end = beginning.UnixMilli(), beginning.AddDate(0, 0, 1).UnixMilli()
		window.counters, window.budgetCounters = snapshot.Day, snapshot.Day
	case domusage.PeriodMonth:
		beginning := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
		window.start, window.end = beginning.UnixMilli(), beginning.AddDate(0, 1, 0).UnixMilli()
		window.counters = snapshot.Month
	}
	return window
}

func (s *Service) periodLimit(period domusage.Period) int64 {
	if s.br == nil {
		return -1
	}
	if period == domusage.PeriodDay {
		return s.br.DailyLimit()
	}
	return s.br.MonthlyLimit()
}

func reportBudget(now time.Time, period domusage.Period, window usageWindow) budget.Budget {
	remaining, resetsAt := int64(-1), int64(0)
	limit := window.limit
	if limit > 0 {
		remaining = max(int64(0), limit-window.budgetCounters.Tokens-window.budgetCounters.Reserved)
		resetsAt = window.end
		if period == domusage.PeriodTotal {
			resetsAt = time.Date(now.Year(), now.Month()+1, 1, 0, 0, 0, 0, time.UTC).UnixMilli()
		}
	} else {
		limit = -1
	}
	return budget.New(int(limit), int(remaining), limit > 0 && remaining == 0, resetsAt)
}
