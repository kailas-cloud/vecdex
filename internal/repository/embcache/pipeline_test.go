package embcache

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/kailas-cloud/vecdex/internal/db"
	"github.com/kailas-cloud/vecdex/internal/domain"
	"go.uber.org/zap"
)

type pipelineStore struct {
	values                              map[string][]byte
	readCalls, writeCalls, scalarWrites int
	readKeys                            []string
	writes                              []db.KVItem
	ttl                                 time.Duration
	readErr, writeErr                   error
}

func (s *pipelineStore) Get(_ context.Context, key string) ([]byte, error) {
	if value, ok := s.values[key]; ok {
		return value, nil
	}
	return nil, db.ErrKeyNotFound
}
func (s *pipelineStore) Set(_ context.Context, key string, value []byte) error {
	s.values[key] = value
	return s.writeErr
}
func (s *pipelineStore) SetWithTTL(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	s.scalarWrites++
	s.ttl = ttl
	return s.Set(ctx, key, value)
}
func (s *pipelineStore) GetMulti(ctx context.Context, keys []string) []db.KVResult {
	s.readCalls++
	s.readKeys = append([]string(nil), keys...)
	out := make([]db.KVResult, len(keys))
	for i, key := range keys {
		out[i].Value, out[i].Err = s.Get(ctx, key)
		if s.readErr != nil {
			out[i].Err = s.readErr
		}
	}
	return out
}
func (s *pipelineStore) SetMultiWithTTL(_ context.Context, items []db.KVItem, ttl time.Duration) error {
	s.writeCalls++
	s.writes = items
	s.ttl = ttl
	for _, item := range items {
		s.values[item.Key] = item.Value
	}
	return s.writeErr
}

func TestPipelineDeduplicatesAndPreservesOrder(t *testing.T) {
	store := &pipelineStore{values: make(map[string][]byte)}
	inner := &mockEmbedder{batchResult: domain.BatchEmbeddingResult{
		Embeddings: [][]float32{{1, 2}, {3, 4}}, TotalTokens: 8}}
	cache := New(inner, store, nil, zap.NewNop()).WithDimensions(2)
	store.values[cache.cacheKey("cached")] = vectorToCacheBytes([]float32{7, 8})
	texts := []string{"a", "b", "a", "cached", "b"}
	result, err := cache.BatchEmbed(context.Background(), texts)
	if err != nil {
		t.Fatal(err)
	}
	if len(store.readKeys) != 3 || store.readCalls != 1 || store.writeCalls != 1 || len(store.writes) != 2 {
		t.Fatalf("unexpected cache operations: %+v", store)
	}
	if len(inner.batchTexts) != 2 || inner.batchTexts[0] != "a" || inner.batchTexts[1] != "b" {
		t.Fatalf("upstream: %v", inner.batchTexts)
	}
	for i, want := range []float32{1, 3, 1, 7, 3} {
		if result.Embeddings[i][0] != want {
			t.Fatalf("order at %d: %v", i, result.Embeddings)
		}
	}
	if result.TotalTokens != 8 || store.ttl != 7*24*time.Hour {
		t.Fatalf("usage or retention: %+v %s", result, store.ttl)
	}
	verifyWarmCache(t, cache, inner, store, texts)
}

func TestPipelinePartialFailureKeepsDuplicatePrefixAndUsage(t *testing.T) {
	cause := errors.New("upstream failure")
	inner := &mockEmbedder{batchErr: cause, batchResult: domain.BatchEmbeddingResult{
		Embeddings: [][]float32{{1, 2}}, TotalTokens: 5, UsageUnknown: true}}
	store := &pipelineStore{values: make(map[string][]byte)}
	cache := New(inner, store, nil, zap.NewNop()).WithDimensions(2)
	store.values[cache.cacheKey("hit")] = vectorToCacheBytes([]float32{9, 9})
	result, err := cache.BatchEmbed(context.Background(), []string{"hit", "a", "a", "b", "hit"})
	if !errors.Is(err, cause) || len(result.Embeddings) != 3 || result.TotalTokens != 5 || !result.UsageUnknown {
		t.Fatalf("partial result lost: %+v %v", result, err)
	}
	if result.Embeddings[0][0] != 9 || result.Embeddings[1][0] != 1 || result.Embeddings[2][0] != 1 ||
		len(store.writes) != 1 {
		t.Fatalf("incorrect partial expansion: %+v", result)
	}
}

func TestCorruptCacheMissAndBestEffortWrite(t *testing.T) {
	corruptValues := [][]byte{
		{1}, vectorToCacheBytes([]float32{1}),
		vectorToCacheBytes([]float32{float32(math.NaN()), 1}),
	}
	for _, corrupt := range corruptValues {
		store := &pipelineStore{values: make(map[string][]byte), writeErr: errors.New("cache unavailable")}
		inner := &mockEmbedder{result: domain.EmbeddingResult{Embedding: []float32{2, 3}, TotalTokens: 4}}
		cache := New(inner, store, nil, zap.NewNop()).WithDimensions(2).WithTTL(time.Hour)
		store.values[cache.cacheKey("text")] = corrupt
		result, err := cache.BatchEmbed(context.Background(), []string{"text"})
		if err != nil || result.TotalTokens != 4 || result.Embeddings[0][0] != 2 {
			t.Fatalf("invalid cache affected embedding: %+v %v", result, err)
		}
		if store.ttl != time.Hour {
			t.Fatal("configured TTL ignored")
		}
	}
}

func verifyWarmCache(t *testing.T, cache *CachedEmbedder, inner *mockEmbedder, store *pipelineStore, texts []string) {
	t.Helper()
	warm, err := cache.BatchEmbed(context.Background(), texts)
	if err != nil {
		t.Fatal(err)
	}
	if warm.TotalTokens != 0 || inner.batchCalls != 1 || store.writeCalls != 1 {
		t.Fatal("warm hit invoked the model or wrote cache")
	}
}
