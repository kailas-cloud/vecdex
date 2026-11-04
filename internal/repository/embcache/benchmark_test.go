package embcache

import (
	"context"
	"crypto/rand"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/kailas-cloud/vecdex/internal/db"
	"github.com/kailas-cloud/vecdex/internal/db/valkey"
	"github.com/kailas-cloud/vecdex/internal/domain"
	"go.uber.org/zap"
)

type measuredCacheStore struct {
	db                 *valkey.Store
	gets, sets, groups int
}

func (s *measuredCacheStore) Get(ctx context.Context, key string) ([]byte, error) {
	s.gets++
	s.groups++
	return s.db.Get(ctx, key)
}
func (s *measuredCacheStore) Set(ctx context.Context, key string, value []byte) error {
	s.sets++
	s.groups++
	return s.db.Set(ctx, key, value)
}
func (s *measuredCacheStore) SetWithTTL(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	s.sets++
	s.groups++
	return s.db.SetWithTTL(ctx, key, value, ttl)
}

type measuredPipeline struct{ *measuredCacheStore }

func (s *measuredPipeline) GetMulti(ctx context.Context, keys []string) []db.KVResult {
	s.gets += len(keys)
	s.groups++
	return s.db.GetMulti(ctx, keys)
}
func (s *measuredPipeline) SetMultiWithTTL(ctx context.Context, items []db.KVItem, ttl time.Duration) error {
	s.sets += len(items)
	s.groups++
	return s.db.SetMultiWithTTL(ctx, items, ttl)
}

// BenchmarkCacheLive compares real Valkey cache access with the former serial,
// per-input algorithm. The deterministic embedder excludes upstream network latency.
func BenchmarkCacheLive(b *testing.B) {
	addr := os.Getenv("VECDEX_TEST_VALKEY_ADDR")
	if addr == "" {
		b.Skip("VECDEX_TEST_VALKEY_ADDR is not set")
	}
	store, err := valkey.NewStore(valkey.Config{Addrs: []string{addr}, Standalone: true})
	if err != nil {
		b.Fatal(err)
	}
	defer store.Close()
	for _, unique := range []int{96, 32} {
		for _, warm := range []bool{false, true} {
			for _, pipeline := range []bool{false, true} {
				name := fmt.Sprintf("unique=%d/warm=%t/pipeline=%t", unique, warm, pipeline)
				b.Run(name, func(b *testing.B) { runCacheBenchmark(b, store, unique, warm, pipeline) })
			}
		}
	}
}

func runCacheBenchmark(b *testing.B, store *valkey.Store, unique int, warm, pipeline bool) {
	measured := &measuredCacheStore{db: store}
	inner := &mockEmbedder{result: domain.EmbeddingResult{Embedding: make([]float32, 1024), TotalTokens: 10}}
	cache := New(inner, measured, nil, zap.NewNop()).WithNamespace(rand.Text()).WithDimensions(1024)
	execute := cache.serialBatchBaseline
	if pipeline {
		cache.store = &measuredPipeline{measured}
		execute = cache.BatchEmbed
	}
	texts := make([]string, 96)
	for i := range texts {
		texts[i] = fmt.Sprintf("benchmark text %d", i%unique)
	}
	clearEntries := func() {
		for i := range unique {
			_ = store.Del(context.Background(), cache.cacheKey(texts[i]))
		}
	}
	defer clearEntries()
	ctx := context.Background()
	if warm {
		if _, err := execute(ctx, texts); err != nil {
			b.Fatal(err)
		}
	}
	measured.gets, measured.sets, measured.groups = 0, 0, 0
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if !warm {
			b.StopTimer()
			clearEntries()
			b.StartTimer()
		}
		if _, err := execute(ctx, texts); err != nil {
			b.Fatal(err)
		}
	}
	b.StopTimer()
	b.ReportMetric(float64(measured.gets)/float64(b.N), "GET/op")
	b.ReportMetric(float64(measured.sets)/float64(b.N), "SET/op")
	b.ReportMetric(float64(measured.groups)/float64(b.N), "cache_batches/op")
}

// serialBatchBaseline preserves the previous per-input read/write algorithm solely
// for comparison; new code always uses BatchEmbed.
func (c *CachedEmbedder) serialBatchBaseline(ctx context.Context, texts []string) (domain.BatchEmbeddingResult, error) {
	embeddings := make([][]float32, len(texts))
	keys := make([]string, len(texts))
	var misses []string
	var indexes []int
	for i, text := range texts {
		keys[i] = c.cacheKey(text)
		if vector, ok := c.getFromCache(ctx, keys[i]); ok {
			embeddings[i] = vector
		} else {
			misses = append(misses, text)
			indexes = append(indexes, i)
		}
	}
	result := domain.BatchEmbeddingResult{}
	if len(misses) > 0 {
		var err error
		result, err = c.embedMisses(ctx, misses)
		if err != nil {
			return result, err
		}
		for i, index := range indexes {
			embeddings[index] = result.Embeddings[i]
			c.putToCache(ctx, keys[index], embeddings[index])
		}
	}
	result.Embeddings = embeddings
	return result, nil
}
