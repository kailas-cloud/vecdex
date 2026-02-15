package embcache

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"math"

	"github.com/prometheus/client_golang/prometheus"
	"go.uber.org/zap"

	"github.com/kailas-cloud/vecdex/internal/db"
	"github.com/kailas-cloud/vecdex/internal/domain"
)

var cacheKeyPrefix = domain.KeyPrefix + "emb_cache:"

// store is the consumer interface for the embedding cache (ISP).
type store interface {
	Get(ctx context.Context, key string) ([]byte, error)
	Set(ctx context.Context, key string, value []byte) error
}

// CachedEmbedder caches embeddings in a key-value store.
type CachedEmbedder struct {
	inner      domain.Embedder
	store      store
	cacheTotal *prometheus.CounterVec
	logger     *zap.Logger
}

// New creates a caching decorator.
// cacheTotal is a counter vec with label "result" ("hit"/"miss"), passed explicitly.
func New(
	inner domain.Embedder,
	s store,
	cacheTotal *prometheus.CounterVec,
	logger *zap.Logger,
) *CachedEmbedder {
	return &CachedEmbedder{
		inner:      inner,
		store:      s,
		cacheTotal: cacheTotal,
		logger:     logger,
	}
}

// Embed returns a cached embedding or calls the inner embedder.
// Cache hit: TotalTokens = 0 (no real tokens consumed).
// Cache miss: full EmbeddingResult from inner.
func (c *CachedEmbedder) Embed(ctx context.Context, text string) (domain.EmbeddingResult, error) {
	key := c.cacheKey(text)

	if vec, ok := c.getFromCache(ctx, key); ok {
		c.incCache("hit")
		return domain.EmbeddingResult{Embedding: vec}, nil
	}

	c.incCache("miss")

	result, err := c.inner.Embed(ctx, text)
	if err != nil {
		return domain.EmbeddingResult{}, fmt.Errorf("embed text: %w", err)
	}

	c.putToCache(ctx, key, result.Embedding)
	return result, nil
}

func (c *CachedEmbedder) incCache(result string) {
	if c.cacheTotal != nil {
		c.cacheTotal.WithLabelValues(result).Inc()
	}
}

func (c *CachedEmbedder) cacheKey(text string) string {
	h := sha256.Sum256([]byte(text))
	return cacheKeyPrefix + hex.EncodeToString(h[:])
}

func (c *CachedEmbedder) getFromCache(ctx context.Context, key string) ([]float32, bool) {
	data, err := c.store.Get(ctx, key)
	if err != nil {
		if !errors.Is(err, db.ErrKeyNotFound) {
			c.logger.Warn("Failed to get cached embedding", zap.String("key", key), zap.Error(err))
		}
		return nil, false
	}
	if len(data) == 0 {
		return nil, false
	}

	vec, err := bytesToVector(data)
	if err != nil {
		c.logger.Warn("Failed to parse cached embedding", zap.String("key", key), zap.Error(err))
		return nil, false
	}

	return vec, true
}

func (c *CachedEmbedder) putToCache(ctx context.Context, key string, vec []float32) {
	data := vectorToCacheBytes(vec)
	if err := c.store.Set(ctx, key, data); err != nil {
		c.logger.Warn("Failed to cache embedding", zap.String("key", key), zap.Error(err))
	}
}

func vectorToCacheBytes(v []float32) []byte {
	buf := make([]byte, len(v)*4)
	for i, f := range v {
		binary.LittleEndian.PutUint32(buf[i*4:], math.Float32bits(f))
	}
	return buf
}

func bytesToVector(data []byte) ([]float32, error) {
	if len(data)%4 != 0 {
		return nil, fmt.Errorf("invalid embedding cache data: len=%d (not multiple of 4)", len(data))
	}
	vec := make([]float32, len(data)/4)
	for i := range vec {
		vec[i] = math.Float32frombits(binary.LittleEndian.Uint32(data[i*4:]))
	}
	return vec, nil
}
