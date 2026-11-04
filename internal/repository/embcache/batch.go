package embcache

import (
	"context"
	"errors"
	"fmt"

	"github.com/kailas-cloud/vecdex/internal/db"
	"github.com/kailas-cloud/vecdex/internal/domain"
	"go.uber.org/zap"
)

type batchPlan struct {
	texts, keys          []string
	inputIndexes, counts []int
}

func (c *CachedEmbedder) planBatch(texts []string) *batchPlan {
	plan := &batchPlan{texts: make([]string, 0, len(texts)), keys: make([]string, 0, len(texts)),
		inputIndexes: make([]int, len(texts)), counts: make([]int, 0, len(texts))}
	indexes := make(map[string]int, len(texts))
	for i, text := range texts {
		index, ok := indexes[text]
		if !ok {
			index = len(plan.texts)
			indexes[text] = index
			plan.texts = append(plan.texts, text)
			plan.keys = append(plan.keys, c.cacheKey(text))
			plan.counts = append(plan.counts, 0)
		}
		plan.inputIndexes[i] = index
		plan.counts[index]++
	}
	return plan
}

// BatchEmbed deduplicates input texts, pipelines cache access and expands vectors
// back into input order. Usage covers only unique misses, including partial failures.
func (c *CachedEmbedder) BatchEmbed(ctx context.Context, texts []string) (domain.BatchEmbeddingResult, error) {
	if len(texts) == 0 {
		return domain.BatchEmbeddingResult{}, nil
	}
	plan := c.planBatch(texts)
	vectors := c.readBatchCache(ctx, plan.keys)
	misses, indexes := c.findBatchMisses(plan, vectors)
	result := domain.BatchEmbeddingResult{}
	var embedErr error
	if len(misses) > 0 {
		result, embedErr = c.embedBatchMisses(ctx, plan, misses, indexes, vectors)
	}
	result.Embeddings = expandBatch(plan, vectors, len(texts), embedErr != nil)
	if embedErr != nil {
		return result, fmt.Errorf("embed unique misses: %w", embedErr)
	}
	return result, nil
}

func (c *CachedEmbedder) findBatchMisses(
	plan *batchPlan,
	vectors [][]float32,
) (missingTexts []string, missingIndexes []int) {
	var misses []string
	var indexes []int
	for i, vector := range vectors {
		label := "hit"
		if vector == nil {
			label = "miss"
			indexes = append(indexes, i)
			misses = append(misses, plan.texts[i])
		}
		if c.cacheTotal != nil {
			c.cacheTotal.WithLabelValues(label).Add(float64(plan.counts[i]))
		}
	}
	return misses, indexes
}

func (c *CachedEmbedder) embedBatchMisses(
	ctx context.Context, plan *batchPlan, misses []string, indexes []int, vectors [][]float32,
) (domain.BatchEmbeddingResult, error) {
	result, embedErr := c.embedMisses(ctx, misses)
	if embedErr == nil && len(result.Embeddings) != len(misses) {
		embedErr = domain.ErrEmbeddingProviderError
		result.Embeddings = nil
	}
	entries := make([]db.KVItem, 0, len(result.Embeddings))
	for j, vector := range result.Embeddings {
		if j >= len(indexes) {
			break
		}
		if err := domain.ValidateVector(vector, c.dimensions); err != nil {
			embedErr = err
			break
		}
		index := indexes[j]
		vectors[index] = vector
		entries = append(entries, db.KVItem{Key: plan.keys[index], Value: vectorToCacheBytes(vector)})
	}
	c.writeBatchCache(ctx, entries)
	return result, embedErr
}

func expandBatch(plan *batchPlan, vectors [][]float32, count int, partial bool) [][]float32 {
	embeddings := make([][]float32, count)
	for position, index := range plan.inputIndexes {
		embeddings[position] = vectors[index]
	}
	if partial {
		end := 0
		for end < len(embeddings) && embeddings[end] != nil {
			end++
		}
		return embeddings[:end]
	}
	return embeddings
}

func (c *CachedEmbedder) readBatchCache(ctx context.Context, keys []string) [][]float32 {
	store, ok := c.store.(db.KVBatchStore)
	if ok {
		return c.decodeBatchCache(store.GetMulti(ctx, keys), len(keys))
	}

	vectors := make([][]float32, len(keys))
	for i, key := range keys {
		vectors[i], _ = c.getFromCache(ctx, key)
	}
	return vectors
}

func (c *CachedEmbedder) writeBatchCache(ctx context.Context, entries []db.KVItem) {
	if len(entries) == 0 {
		return
	}
	if store, ok := c.store.(db.KVBatchStore); ok {
		if err := store.SetMultiWithTTL(ctx, entries, c.ttl); err != nil {
			c.logger.Warn("Cache pipeline write failed", zap.Error(err))
		}
		return
	}
	for _, entry := range entries {
		vector, err := bytesToVector(entry.Value)
		if err == nil {
			c.putToCache(ctx, entry.Key, vector)
		}
	}
}

func (c *CachedEmbedder) decodeBatchCache(replies []db.KVResult, count int) [][]float32 {
	vectors := make([][]float32, count)
	for i, reply := range replies {
		if i >= count {
			break
		}
		if reply.Err != nil {
			if !errors.Is(reply.Err, db.ErrKeyNotFound) {
				c.logger.Warn("Cache pipeline read failed", zap.Error(reply.Err))
			}
			continue
		}
		vector, err := bytesToVector(reply.Value)
		if err == nil && domain.ValidateVector(vector, c.dimensions) == nil {
			vectors[i] = vector
		}
	}
	return vectors
}
