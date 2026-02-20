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
	result      domain.EmbeddingResult
	err         error
	batchResult domain.BatchEmbeddingResult
	batchErr    error
	batchCalls  int
}

func (m *mockEmbedder) Embed(_ context.Context, _ string) (domain.EmbeddingResult, error) {
	return m.result, m.err
}

func (m *mockEmbedder) BatchEmbed(_ context.Context, texts []string) (domain.BatchEmbeddingResult, error) {
	m.batchCalls++
	if m.batchErr != nil {
		return domain.BatchEmbeddingResult{}, m.batchErr
	}
	if m.batchResult.Embeddings != nil {
		return m.batchResult, nil
	}
	embeddings := make([][]float32, len(texts))
	for i := range texts {
		embeddings[i] = m.result.Embedding
	}
	return domain.BatchEmbeddingResult{
		Embeddings:   embeddings,
		PromptTokens: m.result.PromptTokens * len(texts),
		TotalTokens:  m.result.TotalTokens * len(texts),
	}, nil
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

// --- BatchEmbed tests ---

func TestInstrumentedEmbedder_BatchEmbed_Success(t *testing.T) {
	inner := &mockEmbedder{result: domain.EmbeddingResult{
		Embedding:    []float32{0.1, 0.2},
		PromptTokens: 10,
		TotalTokens:  10,
	}}
	p := NewInstrumentedEmbedder(inner, "test-batch", "test-model-b", nil, zap.NewNop())

	res, err := p.BatchEmbed(context.Background(), []string{"a", "b", "c"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(res.Embeddings) != 3 {
		t.Fatalf("expected 3 embeddings, got %d", len(res.Embeddings))
	}
	if inner.batchCalls != 1 {
		t.Errorf("expected 1 batch call, got %d", inner.batchCalls)
	}
}

func TestInstrumentedEmbedder_BatchEmbed_Empty(t *testing.T) {
	inner := &mockEmbedder{}
	p := NewInstrumentedEmbedder(inner, "test", "test-model", nil, zap.NewNop())

	res, err := p.BatchEmbed(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Embeddings != nil {
		t.Errorf("expected nil for empty input")
	}
}

func TestInstrumentedEmbedder_BatchEmbed_BudgetRejection(t *testing.T) {
	budget := NewBudgetTracker("test-batch-budget", 100, 0, BudgetActionReject, zap.NewNop())
	budget.Record(100)

	inner := &mockEmbedder{result: domain.EmbeddingResult{Embedding: []float32{0.1}}}
	p := NewInstrumentedEmbedder(inner, "test-batch-budget", "model", budget, zap.NewNop())

	_, err := p.BatchEmbed(context.Background(), []string{"a", "b"})
	if err == nil {
		t.Fatal("expected budget rejection error")
	}
	if !errors.Is(err, domain.ErrEmbeddingQuotaExceeded) {
		t.Errorf("expected ErrEmbeddingQuotaExceeded, got %v", err)
	}
}

func TestInstrumentedEmbedder_BatchEmbed_RecordsBudget(t *testing.T) {
	budget := NewBudgetTracker("test-batch-rec", 1000000, 10000000, BudgetActionReject, zap.NewNop())

	inner := &mockEmbedder{result: domain.EmbeddingResult{
		Embedding:    []float32{0.1},
		PromptTokens: 100,
		TotalTokens:  100,
	}}
	p := NewInstrumentedEmbedder(inner, "test-batch-rec", "model", budget, zap.NewNop())

	initialDaily := budget.RemainingDaily()

	_, err := p.BatchEmbed(context.Background(), []string{"a", "b", "c"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// 3 texts * 100 tokens = 300
	expectedDecrease := int64(300)
	actual := initialDaily - budget.RemainingDaily()
	if actual != expectedDecrease {
		t.Errorf("expected budget decrease of %d, got %d", expectedDecrease, actual)
	}
}

func TestInstrumentedEmbedder_BatchEmbed_InnerError(t *testing.T) {
	inner := &mockEmbedder{
		result:   domain.EmbeddingResult{Embedding: []float32{0.1}},
		batchErr: fmt.Errorf("api error"),
	}
	p := NewInstrumentedEmbedder(inner, "test-err", "model", nil, zap.NewNop())

	_, err := p.BatchEmbed(context.Background(), []string{"a"})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestInstrumentedEmbedder_BatchEmbed_FallbackToSingle(t *testing.T) {
	// Inner без BatchEmbedder — fallback
	inner := &plainMockEmbedder{result: domain.EmbeddingResult{
		Embedding:    []float32{0.1},
		PromptTokens: 5,
		TotalTokens:  5,
	}}
	p := NewInstrumentedEmbedder(inner, "test-fb", "model", nil, zap.NewNop())

	res, err := p.BatchEmbed(context.Background(), []string{"a", "b"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(res.Embeddings) != 2 {
		t.Fatalf("expected 2 embeddings, got %d", len(res.Embeddings))
	}
	if inner.calls != 2 {
		t.Errorf("expected 2 fallback Embed calls, got %d", inner.calls)
	}
}

// plainMockEmbedder implements only Embedder, not BatchEmbedder.
type plainMockEmbedder struct {
	result domain.EmbeddingResult
	err    error
	calls  int
}

func (m *plainMockEmbedder) Embed(_ context.Context, _ string) (domain.EmbeddingResult, error) {
	m.calls++
	return m.result, m.err
}
