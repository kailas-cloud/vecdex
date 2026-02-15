package embedding

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"

	"go.uber.org/zap"

	"github.com/kailas-cloud/vecdex/internal/domain"
	"github.com/kailas-cloud/vecdex/internal/metrics"
)

func TestMain(m *testing.M) {
	metrics.RegisterEmbeddingMetrics()
	os.Exit(m.Run())
}

type mockEmbedder struct {
	result domain.EmbeddingResult
	err    error
}

func (m *mockEmbedder) Embed(_ context.Context, _ string) (domain.EmbeddingResult, error) {
	return m.result, m.err
}

func TestInstrumentedEmbedder_Success(t *testing.T) {
	inner := &mockEmbedder{result: domain.EmbeddingResult{
		Embedding: []float32{0.1, 0.2, 0.3},
	}}
	p := NewInstrumentedEmbedder(inner, "test", "test-model", nil, zap.NewNop())

	result, err := p.Embed(context.Background(), "hello")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Embedding) != 3 {
		t.Fatalf("expected 3 dimensions, got %d", len(result.Embedding))
	}
}

func TestInstrumentedEmbedder_WithUsage(t *testing.T) {
	inner := &mockEmbedder{result: domain.EmbeddingResult{
		Embedding:    []float32{0.1, 0.2},
		PromptTokens: 100,
		TotalTokens:  100,
	}}
	p := NewInstrumentedEmbedder(inner, "test-usage", "test-model-u", nil, zap.NewNop())

	result, err := p.Embed(context.Background(), "hello")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Embedding) != 2 {
		t.Fatalf("expected 2 dimensions, got %d", len(result.Embedding))
	}
	if result.TotalTokens != 100 {
		t.Fatalf("expected 100 total tokens, got %d", result.TotalTokens)
	}
}

func TestInstrumentedEmbedder_Error(t *testing.T) {
	inner := &mockEmbedder{err: fmt.Errorf("api error")}
	p := NewInstrumentedEmbedder(inner, "test-err", "test-model-e", nil, zap.NewNop())

	_, err := p.Embed(context.Background(), "hello")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestInstrumentedEmbedder_BudgetRejection(t *testing.T) {
	budget := NewBudgetTracker("test-budget", 100, 0, BudgetActionReject, zap.NewNop())
	budget.Record(100)

	inner := &mockEmbedder{result: domain.EmbeddingResult{
		Embedding: []float32{0.1, 0.2, 0.3},
	}}
	p := NewInstrumentedEmbedder(inner, "test-budget", "test-model-b", budget, zap.NewNop())

	_, err := p.Embed(context.Background(), "hello")
	if err == nil {
		t.Fatal("expected error when budget exceeded")
	}
	if !errors.Is(err, domain.ErrEmbeddingQuotaExceeded) {
		t.Fatalf("expected domain.ErrEmbeddingQuotaExceeded, got %v", err)
	}
}

func TestInstrumentedEmbedder_RecordsBudgetAndMetrics(t *testing.T) {
	budget := NewBudgetTracker("test-record", 1000000, 10000000, BudgetActionReject, zap.NewNop())

	inner := &mockEmbedder{result: domain.EmbeddingResult{
		Embedding:    []float32{0.1, 0.2, 0.3},
		PromptTokens: 500,
		TotalTokens:  500,
	}}
	p := NewInstrumentedEmbedder(inner, "test-record", "test-model-r", budget, zap.NewNop())

	initialDaily := budget.RemainingDaily()
	initialMonthly := budget.RemainingMonthly()

	result, err := p.Embed(context.Background(), "hello")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Embedding) != 3 {
		t.Fatalf("expected 3 dimensions, got %d", len(result.Embedding))
	}

	newDaily := budget.RemainingDaily()
	newMonthly := budget.RemainingMonthly()

	if newDaily != initialDaily-500 {
		t.Errorf("expected daily remaining to decrease by 500, got %d -> %d", initialDaily, newDaily)
	}
	if newMonthly != initialMonthly-500 {
		t.Errorf("expected monthly remaining to decrease by 500, got %d -> %d", initialMonthly, newMonthly)
	}
}
