package usage

import (
	"testing"

	"github.com/kailas-cloud/vecdex/internal/domain/usage/budget"
	"github.com/kailas-cloud/vecdex/internal/domain/usage/metrics"
)

func TestNewReport(t *testing.T) {
	m := metrics.New(1542, 384200, 38)
	b := budget.New(1000000, 615800, false, 1700000000000)

	r := NewReport(PeriodMonth, 1700000000, 1702600000, "code-chunks", m, b)

	if r.Period() != PeriodMonth {
		t.Errorf("Period() = %q", r.Period())
	}
	if r.PeriodStart() != 1700000000 {
		t.Errorf("PeriodStart() = %d", r.PeriodStart())
	}
	if r.PeriodEnd() != 1702600000 {
		t.Errorf("PeriodEnd() = %d", r.PeriodEnd())
	}
	if r.Collection() != "code-chunks" {
		t.Errorf("Collection() = %q", r.Collection())
	}
	if r.Metrics().EmbeddingRequests() != 1542 {
		t.Errorf("Metrics().EmbeddingRequests() = %d", r.Metrics().EmbeddingRequests())
	}
	if r.Budget().TokensLimit() != 1000000 {
		t.Errorf("Budget().TokensLimit() = %d", r.Budget().TokensLimit())
	}
}

func TestPeriodConstants(t *testing.T) {
	if PeriodDay != "day" {
		t.Errorf("PeriodDay = %q", PeriodDay)
	}
	if PeriodMonth != "month" {
		t.Errorf("PeriodMonth = %q", PeriodMonth)
	}
	if PeriodTotal != "total" {
		t.Errorf("PeriodTotal = %q", PeriodTotal)
	}
}
