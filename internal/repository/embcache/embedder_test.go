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
