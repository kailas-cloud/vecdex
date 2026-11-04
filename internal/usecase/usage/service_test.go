package usage

import (
	"context"
	"errors"
	"testing"

	domusage "github.com/kailas-cloud/vecdex/internal/domain/usage"
)

type reportReader struct {
	counters domusage.Snapshot
	err      error
}

func (r *reportReader) DailyLimit() int64                                   { return 100 }
func (r *reportReader) MonthlyLimit() int64                                 { return 1000 }
func (r *reportReader) Snapshot(context.Context) (domusage.Snapshot, error) { return r.counters, r.err }

func TestReportsUseIndependentPeriodsAndReservations(t *testing.T) {
	reader := &reportReader{counters: domusage.Snapshot{
		Day:   domusage.Counters{Tokens: 10, Reserved: 90, Requests: 2, Unknown: 1},
		Month: domusage.Counters{Tokens: 200, Reserved: 100, Requests: 12},
		Total: domusage.Counters{Tokens: 9000, Requests: 200, Unknown: 3},
	}}
	svc := New(reader)
	for _, tc := range []struct {
		period                               domusage.Period
		tokens, requests, unknown, remaining int
	}{
		{domusage.PeriodDay, 10, 2, 1, 0}, {domusage.PeriodMonth, 200, 12, 0, 700},
		{domusage.PeriodTotal, 9000, 200, 3, 700},
	} {
		t.Run(string(tc.period), func(t *testing.T) {
			report, err := svc.GetReport(context.Background(), tc.period)
			if err != nil {
				t.Fatal(err)
			}
			m := report.Metrics()
			if m.Tokens() != tc.tokens || m.EmbeddingRequests() != tc.requests || m.UnknownRequests() != tc.unknown {
				t.Fatalf("wrong usage: %+v", m)
			}
			if report.Budget().TokensRemaining() != tc.remaining {
				t.Fatalf("wrong remaining: %+v", report.Budget())
			}
			if (report.PeriodStart() == 0) != (tc.period == domusage.PeriodTotal) {
				t.Fatal("incorrect period boundaries")
			}
			if report.Budget().IsExhausted() != (tc.remaining == 0) {
				t.Fatal("reservation not reflected in budget")
			}
		})
	}
}

func TestUnlimitedReportAndReadFailure(t *testing.T) {
	report, err := New(nil).GetReport(context.Background(), domusage.PeriodDay)
	if err != nil {
		t.Fatal(err)
	}
	if report.Budget().TokensLimit() != -1 || report.Budget().TokensRemaining() != -1 || report.Budget().ResetsAt() != 0 {
		t.Fatalf("incorrect unlimited report: %+v", report.Budget())
	}
	cause := errors.New("store unavailable")
	_, err = New(&reportReader{err: cause}).GetReport(context.Background(), domusage.PeriodMonth)
	if !errors.Is(err, cause) {
		t.Fatalf("read failure was hidden: %v", err)
	}
}
