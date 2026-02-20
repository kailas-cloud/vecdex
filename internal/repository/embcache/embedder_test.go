package embcache

import (
	"context"
	"errors"
	"testing"

	"github.com/kailas-cloud/vecdex/internal/db"
	"github.com/kailas-cloud/vecdex/internal/domain"
)

func TestEmbed_CacheMiss(t *testing.T) {
	inner := &mockEmbedder{result: domain.EmbeddingResult{
		Embedding:    []float32{0.1, 0.2, 0.3},
		PromptTokens: 10,
		TotalTokens:  10,
	}}
	ce, ms := newTestCachedEmbedder(t, inner)
	ctx := context.Background()

	// GET → ErrKeyNotFound (cache miss)
	ms.getFn = func(_ context.Context, _ string) ([]byte, error) {
		return nil, db.ErrKeyNotFound
	}

	// SET → OK (cache put)
	var setCalled bool
	ms.setFn = func(_ context.Context, _ string, _ []byte) error {
		setCalled = true
		return nil
	}

	result, err := ce.Embed(ctx, "test text")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Embedding) != 3 || result.Embedding[0] != 0.1 {
		t.Fatalf("unexpected vector: %v", result.Embedding)
	}
	if result.TotalTokens != 10 {
		t.Fatalf("expected TotalTokens=10, got %d", result.TotalTokens)
	}
	if !setCalled {
		t.Fatal("expected SET to be called for cache put")
	}
}

func TestEmbed_CacheHit(t *testing.T) {
	inner := &mockEmbedder{result: domain.EmbeddingResult{
		Embedding: []float32{0.1, 0.2, 0.3},
	}}
	ce, ms := newTestCachedEmbedder(t, inner)
	ctx := context.Background()

	cached := vectorToCacheBytes([]float32{0.4, 0.5, 0.6})

	// GET → cached bytes
	ms.getFn = func(_ context.Context, _ string) ([]byte, error) {
		return cached, nil
	}

	result, err := ce.Embed(ctx, "test text")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Embedding) != 3 || result.Embedding[0] != 0.4 {
		t.Fatalf("expected cached vector, got: %v", result.Embedding)
	}
	if result.TotalTokens != 0 {
		t.Fatalf("expected TotalTokens=0 on cache hit, got %d", result.TotalTokens)
	}
}

func TestEmbed_InnerError(t *testing.T) {
	inner := &mockEmbedder{err: errors.New("provider down")}
	ce, ms := newTestCachedEmbedder(t, inner)
	ctx := context.Background()

	// GET → ErrKeyNotFound (cache miss)
	ms.getFn = func(_ context.Context, _ string) ([]byte, error) {
		return nil, db.ErrKeyNotFound
	}

	_, err := ce.Embed(ctx, "test text")
	if err == nil {
		t.Fatal("expected error from inner embedder")
	}
}

// --- BatchEmbed tests ---

func TestBatchEmbed_AllMisses(t *testing.T) {
	inner := &mockEmbedder{result: domain.EmbeddingResult{
		Embedding:    []float32{0.1, 0.2},
		PromptTokens: 5,
		TotalTokens:  5,
	}}
	ce, ms := newTestCachedEmbedder(t, inner)

	ms.getFn = func(_ context.Context, _ string) ([]byte, error) {
		return nil, db.ErrKeyNotFound
	}
	var setCount int
	ms.setFn = func(_ context.Context, _ string, _ []byte) error {
		setCount++
		return nil
	}

	res, err := ce.BatchEmbed(context.Background(), []string{"a", "b"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(res.Embeddings) != 2 {
		t.Fatalf("expected 2 embeddings, got %d", len(res.Embeddings))
	}
	if setCount != 2 {
		t.Errorf("expected 2 cache puts, got %d", setCount)
	}
	if inner.batchCalls != 1 {
		t.Errorf("expected 1 batch call to inner, got %d", inner.batchCalls)
	}
	if res.TotalTokens != 10 {
		t.Errorf("expected TotalTokens=10, got %d", res.TotalTokens)
	}
}

func TestBatchEmbed_AllHits(t *testing.T) {
	inner := &mockEmbedder{result: domain.EmbeddingResult{Embedding: []float32{0.1}}}
	ce, ms := newTestCachedEmbedder(t, inner)

	cached := vectorToCacheBytes([]float32{0.9, 0.8})
	ms.getFn = func(_ context.Context, _ string) ([]byte, error) {
		return cached, nil
	}

	res, err := ce.BatchEmbed(context.Background(), []string{"a", "b"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(res.Embeddings) != 2 {
		t.Fatalf("expected 2 embeddings, got %d", len(res.Embeddings))
	}
	// Все из кеша — 0 токенов, 0 вызовов inner
	if res.TotalTokens != 0 {
		t.Errorf("expected TotalTokens=0 on all hits, got %d", res.TotalTokens)
	}
	if inner.batchCalls != 0 {
		t.Errorf("expected 0 batch calls (all cache hits), got %d", inner.batchCalls)
	}
}

func TestBatchEmbed_MixedHitsMisses(t *testing.T) {
	inner := &mockEmbedder{result: domain.EmbeddingResult{
		Embedding:    []float32{0.5},
		PromptTokens: 3,
		TotalTokens:  3,
	}}
	ce, ms := newTestCachedEmbedder(t, inner)

	cachedVec := vectorToCacheBytes([]float32{0.9})
	callNum := 0
	ms.getFn = func(_ context.Context, _ string) ([]byte, error) {
		callNum++
		if callNum == 2 { // second text is cached
			return cachedVec, nil
		}
		return nil, db.ErrKeyNotFound
	}
	ms.setFn = func(_ context.Context, _ string, _ []byte) error { return nil }

	res, err := ce.BatchEmbed(context.Background(), []string{"miss1", "hit1", "miss2"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(res.Embeddings) != 3 {
		t.Fatalf("expected 3 embeddings, got %d", len(res.Embeddings))
	}
	// hit1 returns cached vec
	if res.Embeddings[1][0] != 0.9 {
		t.Errorf("expected cached vec for index 1, got %v", res.Embeddings[1])
	}
	// misses get inner result
	if res.Embeddings[0][0] != 0.5 || res.Embeddings[2][0] != 0.5 {
		t.Errorf("expected inner vec for misses, got %v, %v", res.Embeddings[0], res.Embeddings[2])
	}
	// Only misses consume tokens
	if res.TotalTokens != 6 {
		t.Errorf("expected TotalTokens=6 (2 misses * 3), got %d", res.TotalTokens)
	}
}

func TestBatchEmbed_InnerError(t *testing.T) {
	inner := &mockEmbedder{
		result:   domain.EmbeddingResult{Embedding: []float32{0.1}},
		batchErr: errors.New("api down"),
	}
	ce, ms := newTestCachedEmbedder(t, inner)

	ms.getFn = func(_ context.Context, _ string) ([]byte, error) {
		return nil, db.ErrKeyNotFound
	}

	_, err := ce.BatchEmbed(context.Background(), []string{"a"})
	if err == nil {
		t.Fatal("expected error from inner batch embedder")
	}
}

func TestBatchEmbed_Empty(t *testing.T) {
	inner := &mockEmbedder{}
	ce, _ := newTestCachedEmbedder(t, inner)

	res, err := ce.BatchEmbed(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Embeddings != nil {
		t.Errorf("expected nil for empty input")
	}
}
