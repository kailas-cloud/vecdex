package embedding

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/kailas-cloud/vecdex/internal/domain"
)

type batchStubEmbedder struct {
	mu            sync.Mutex
	maxConcurrent int
	inFlight      int
	batchCalls    [][]string
	sleepPerText  time.Duration
	batchErr      error
}

func (s *batchStubEmbedder) Embed(_ context.Context, text string) (domain.EmbeddingResult, error) {
	return domain.EmbeddingResult{
		Embedding:    []float32{float32(len(text))},
		PromptTokens: 1,
		TotalTokens:  1,
	}, nil
}

func (s *batchStubEmbedder) BatchEmbed(ctx context.Context, texts []string) (domain.BatchEmbeddingResult, error) {
	s.mu.Lock()
	s.inFlight++
	if s.inFlight > s.maxConcurrent {
		s.maxConcurrent = s.inFlight
	}
	s.batchCalls = append(s.batchCalls, append([]string(nil), texts...))
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		s.inFlight--
		s.mu.Unlock()
	}()

	if s.sleepPerText > 0 {
		timer := time.NewTimer(time.Duration(len(texts)) * s.sleepPerText)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return domain.BatchEmbeddingResult{}, ctx.Err()
		case <-timer.C:
		}
	}

	if s.batchErr != nil {
		return domain.BatchEmbeddingResult{}, s.batchErr
	}

	result := domain.BatchEmbeddingResult{
		Embeddings:   make([][]float32, len(texts)),
		PromptTokens: len(texts),
		TotalTokens:  len(texts),
	}
	for i, text := range texts {
		result.Embeddings[i] = []float32{float32(len(text))}
	}
	return result, nil
}

func TestParallelBatchEmbedderPreservesOrder(t *testing.T) {
	inner := &batchStubEmbedder{}
	embedder := NewParallelBatchEmbedder(inner, 2, 3)

	res, err := embedder.BatchEmbed(context.Background(), []string{
		"one", "three", "seven", "eleven", "thirteen",
	})
	if err != nil {
		t.Fatalf("BatchEmbed() error = %v", err)
	}

	if len(res.Embeddings) != 5 {
		t.Fatalf("len(Embeddings) = %d, want 5", len(res.Embeddings))
	}
	for i, want := range []float32{3, 5, 5, 6, 8} {
		if got := res.Embeddings[i][0]; got != want {
			t.Fatalf("Embeddings[%d][0] = %v, want %v", i, got, want)
		}
	}
	if res.TotalTokens != 5 || res.PromptTokens != 5 {
		t.Fatalf("tokens = (%d, %d), want (5, 5)", res.PromptTokens, res.TotalTokens)
	}
}

func TestParallelBatchEmbedderUsesConcurrency(t *testing.T) {
	inner := &batchStubEmbedder{sleepPerText: 5 * time.Millisecond}
	embedder := NewParallelBatchEmbedder(inner, 10, 10)

	texts := make([]string, 100)
	for i := range texts {
		texts[i] = "abcdefghij"
	}

	start := time.Now()
	_, err := embedder.BatchEmbed(context.Background(), texts)
	if err != nil {
		t.Fatalf("BatchEmbed() error = %v", err)
	}
	elapsed := time.Since(start)

	if inner.maxConcurrent < 5 {
		t.Fatalf("maxConcurrent = %d, want >= 5", inner.maxConcurrent)
	}
	if elapsed >= 120*time.Millisecond {
		t.Fatalf("elapsed = %s, want < 120ms", elapsed)
	}
}

func TestParallelBatchEmbedderPropagatesError(t *testing.T) {
	inner := &batchStubEmbedder{batchErr: errors.New("boom")}
	embedder := NewParallelBatchEmbedder(inner, 2, 2)

	_, err := embedder.BatchEmbed(context.Background(), []string{"a", "b", "c"})
	if err == nil {
		t.Fatal("BatchEmbed() expected error")
	}
}
