package usage

import (
	"context"
	"testing"
	"time"

	domusage "github.com/kailas-cloud/vecdex/internal/domain/usage"
)

// --- Mock ---

type mockBudgetReader struct {
	dailyLimit       int64
	monthlyLimit     int64
	dailyUsed        int64
	monthlyUsed      int64
	remainingDaily   int64
	remainingMonthly int64
}

func (m *mockBudgetReader) DailyLimit() int64       { return m.dailyLimit }
func (m *mockBudgetReader) MonthlyLimit() int64     { return m.monthlyLimit }
func (m *mockBudgetReader) DailyUsed() int64        { return m.dailyUsed }
func (m *mockBudgetReader) MonthlyUsed() int64      { return m.monthlyUsed }
func (m *mockBudgetReader) RemainingDaily() int64   { return m.remainingDaily }
func (m *mockBudgetReader) RemainingMonthly() int64 { return m.remainingMonthly }

// --- Tests ---

func TestGetReport_DailyPeriod(t *testing.T) {
	br := &mockBudgetReader{
		dailyLimit:       10000,
		dailyUsed:        3000,
		remainingDaily:   7000,
		monthlyLimit:     100000,
		monthlyUsed:      50000,
		remainingMonthly: 50000,
	}
	svc := New(br)
	r := svc.GetReport(context.Background(), domusage.PeriodDay)

	if r.Period() != domusage.PeriodDay {
		t.Errorf("expected period %q, got %q", domusage.PeriodDay, r.Period())
	}

	now := time.Now().UTC()
	dayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	if r.PeriodStart() != dayStart.UnixMilli() {
		t.Errorf("expected period start %d, got %d", dayStart.UnixMilli(), r.PeriodStart())
	}

	dayEnd := dayStart.Add(24 * time.Hour)
	if r.PeriodEnd() != dayEnd.UnixMilli() {
		t.Errorf("expected period end %d, got %d", dayEnd.UnixMilli(), r.PeriodEnd())
	}

	if r.Budget().TokensLimit() != 10000 {
		t.Errorf("expected limit 10000, got %d", r.Budget().TokensLimit())
	}
	if r.Budget().TokensRemaining() != 7000 {
		t.Errorf("expected remaining 7000, got %d", r.Budget().TokensRemaining())
	}
	if r.Budget().IsExhausted() {
		t.Error("budget should not be exhausted")
	}
	if r.Metrics().Tokens() != 3000 {
		t.Errorf("expected tokens 3000, got %d", r.Metrics().Tokens())
	}
}

func TestGetReport_MonthlyPeriod(t *testing.T) {
	br := &mockBudgetReader{
		monthlyLimit:     100000,
		monthlyUsed:      80000,
		remainingMonthly: 20000,
	}
	svc := New(br)
	r := svc.GetReport(context.Background(), domusage.PeriodMonth)

	if r.Period() != domusage.PeriodMonth {
		t.Errorf("expected period %q, got %q", domusage.PeriodMonth, r.Period())
	}

	now := time.Now().UTC()
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	if r.PeriodStart() != monthStart.UnixMilli() {
		t.Errorf("expected period start %d, got %d", monthStart.UnixMilli(), r.PeriodStart())
	}

	monthEnd := monthStart.AddDate(0, 1, 0)
	if r.PeriodEnd() != monthEnd.UnixMilli() {
		t.Errorf("expected period end %d, got %d", monthEnd.UnixMilli(), r.PeriodEnd())
	}

	if r.Budget().TokensLimit() != 100000 {
		t.Errorf("expected limit 100000, got %d", r.Budget().TokensLimit())
	}
}

func TestGetReport_TotalPeriod(t *testing.T) {
	br := &mockBudgetReader{
		monthlyLimit:     100000,
		monthlyUsed:      100000,
		remainingMonthly: 0,
	}
	svc := New(br)
	r := svc.GetReport(context.Background(), domusage.PeriodTotal)

	if r.Period() != domusage.PeriodTotal {
		t.Errorf("expected period %q, got %q", domusage.PeriodTotal, r.Period())
	}

	// total period — no boundaries
	if r.PeriodStart() != 0 {
		t.Errorf("expected period start 0 for total, got %d", r.PeriodStart())
	}
	if r.PeriodEnd() != 0 {
		t.Errorf("expected period end 0 for total, got %d", r.PeriodEnd())
	}

	if r.Budget().TokensLimit() != 100000 {
		t.Errorf("expected limit 100000, got %d", r.Budget().TokensLimit())
	}
}

func TestGetReport_NilBudgetReader(t *testing.T) {
	svc := New(nil)
	r := svc.GetReport(context.Background(), domusage.PeriodDay)

	if r.Budget().TokensLimit() != 0 {
		t.Errorf("expected limit 0, got %d", r.Budget().TokensLimit())
	}
	if r.Budget().TokensRemaining() != 0 {
		t.Errorf("expected remaining 0, got %d", r.Budget().TokensRemaining())
	}
	if r.Budget().IsExhausted() {
		t.Error("nil budget reader should not be exhausted")
	}
}

func TestGetReport_Exhausted(t *testing.T) {
	br := &mockBudgetReader{
		dailyLimit:     5000,
		dailyUsed:      5000,
		remainingDaily: 0,
	}
	svc := New(br)
	r := svc.GetReport(context.Background(), domusage.PeriodDay)

	if !r.Budget().IsExhausted() {
		t.Error("budget should be exhausted when remaining is 0")
	}
}
