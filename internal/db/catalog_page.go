package db

import (
	"container/heap"
	"context"
	"fmt"
)

// PartitionCatalogStore pipelines catalog reads across independent hash slots.
type PartitionCatalogStore interface {
	CatalogIDPages(ctx context.Context, keys []string, after string, limit int) ([][]string, error)
	CatalogCounts(ctx context.Context, keys []string) ([]int, error)
}

const catalogBlockSize = 8

// ReadCatalogPage merges a bounded lexicographic window from partition catalogs.
// The caller's cursor is the last consumed ID, so no server-side cursor is needed.
// Memory is O(partitions*8 + limit), rather than O(partitions*limit).
func ReadCatalogPage(
	ctx context.Context, store PartitionCatalogStore, keys []string, after string, limit int,
) ([]string, error) {
	if limit <= 0 {
		return nil, fmt.Errorf("catalog page size must be positive")
	}
	if len(keys) == 0 {
		return []string{}, nil
	}
	pages, err := store.CatalogIDPages(ctx, keys, after, catalogBlockSize)
	if err != nil {
		return nil, fmt.Errorf("read catalog heads: %w", err)
	}
	if len(pages) != len(keys) {
		return nil, fmt.Errorf("catalog head count mismatch")
	}
	pending, err := catalogHeads(pages, after)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, min(limit, catalogBlockSize))
	for len(pending) > 0 && len(out) < limit {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("merge catalog page: %w", err)
		}
		head := heap.Pop(&pending).(catalogHead)
		if len(out) > 0 && out[len(out)-1] == head.id {
			return nil, fmt.Errorf("duplicate source ID across catalogs")
		}
		out = append(out, head.id)
		if len(out) == limit {
			break
		}
		next, err := advanceCatalogHead(ctx, store, keys[head.partition], pages[head.partition], head)
		if err != nil {
			return nil, err
		}
		pages[head.partition] = next.page
		if len(next.page) > 0 {
			heap.Push(&pending, next.head)
		}
	}
	return out, nil
}

func catalogHeads(pages [][]string, after string) (catalogHeap, error) {
	pending := make(catalogHeap, 0, len(pages))
	for partition, page := range pages {
		if err := validateCatalogBlock(page, after); err != nil {
			return nil, err
		}
		if len(page) != 0 {
			pending = append(pending, catalogHead{id: page[0], partition: partition})
		}
	}
	heap.Init(&pending)
	return pending, nil
}

type catalogAdvance struct {
	page []string
	head catalogHead
}

func advanceCatalogHead(
	ctx context.Context, store PartitionCatalogStore, key string, page []string, head catalogHead,
) (catalogAdvance, error) {
	head.offset++
	if head.offset < len(page) {
		head.id = page[head.offset]
		return catalogAdvance{page: page, head: head}, nil
	}
	if len(page) < catalogBlockSize {
		return catalogAdvance{}, nil
	}
	refill, err := store.CatalogIDPages(ctx, []string{key}, head.id, catalogBlockSize)
	if err != nil {
		return catalogAdvance{}, fmt.Errorf("refill partition catalog: %w", err)
	}
	if len(refill) != 1 {
		return catalogAdvance{}, fmt.Errorf("catalog refill count mismatch")
	}
	if err := validateCatalogBlock(refill[0], head.id); err != nil {
		return catalogAdvance{}, err
	}
	if len(refill[0]) == 0 {
		return catalogAdvance{}, nil
	}
	head.id, head.offset = refill[0][0], 0
	return catalogAdvance{page: refill[0], head: head}, nil
}

func validateCatalogBlock(page []string, after string) error {
	if len(page) > catalogBlockSize {
		return fmt.Errorf("catalog block exceeds requested size")
	}
	for _, id := range page {
		if id <= after {
			return fmt.Errorf("catalog IDs are not strictly increasing")
		}
		after = id
	}
	return nil
}

type catalogHead struct {
	id        string
	partition int
	offset    int
}

type catalogHeap []catalogHead

func (h catalogHeap) Len() int           { return len(h) }
func (h catalogHeap) Less(i, j int) bool { return h[i].id < h[j].id }
func (h catalogHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *catalogHeap) Push(value any)    { *h = append(*h, value.(catalogHead)) }
func (h *catalogHeap) Pop() any {
	old := *h
	last := old[len(old)-1]
	*h = old[:len(old)-1]
	return last
}
