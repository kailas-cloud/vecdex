package embcache

import (
	"context"
	"crypto/rand"
	"os"
	"testing"
	"time"

	"github.com/kailas-cloud/vecdex/internal/db/valkey"
	"github.com/kailas-cloud/vecdex/internal/domain"
	"go.uber.org/zap"
)

func TestCacheTTLLive(t *testing.T) {
	addr := os.Getenv("VECDEX_TEST_VALKEY_ADDR")
	if addr == "" {
		t.Skip("VECDEX_TEST_VALKEY_ADDR is not set")
	}
	store, err := valkey.NewStore(valkey.Config{Addrs: []string{addr}, Standalone: true})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	inner := &mockEmbedder{result: domain.EmbeddingResult{Embedding: []float32{1, 2}, TotalTokens: 4}}
	cache := New(
		inner,
		store,
		nil,
		zap.NewNop(),
	).WithNamespace(rand.Text()).
		WithDimensions(2).
		WithTTL(200 * time.Millisecond)
	ctx := context.Background()
	defer func() {
		for _, text := range []string{"scalar", "batch-a", "batch-b"} {
			_ = store.Del(ctx, cache.cacheKey(text))
		}
	}()
	verifyScalarWarmHit(t, cache, inner)
	texts := []string{"batch-a", "batch-b", "batch-a"}
	if _, err := cache.BatchEmbed(ctx, texts); err != nil {
		t.Fatal(err)
	}
	batchWarm, err := cache.BatchEmbed(ctx, texts)
	if err != nil || batchWarm.TotalTokens != 0 || inner.batchCalls != 1 {
		t.Fatalf("pipeline hit: %+v %v calls %d", batchWarm, err, inner.batchCalls)
	}
	time.Sleep(250 * time.Millisecond)
	if _, err := cache.Embed(ctx, "scalar"); err != nil {
		t.Fatal(err)
	}
	if _, err := cache.BatchEmbed(ctx, texts); err != nil {
		t.Fatal(err)
	}
	if inner.embedCalls != 2 || inner.batchCalls != 2 {
		t.Fatal("expired entries remained cached")
	}
}

func verifyScalarWarmHit(t *testing.T, cache *CachedEmbedder, inner *mockEmbedder) {
	t.Helper()
	ctx := context.Background()
	if _, err := cache.Embed(ctx, "scalar"); err != nil {
		t.Fatal(err)
	}
	warm, err := cache.Embed(ctx, "scalar")
	if err != nil || warm.TotalTokens != 0 || inner.embedCalls != 1 {
		t.Fatalf("scalar hit: %+v %v calls %d", warm, err, inner.embedCalls)
	}
}
