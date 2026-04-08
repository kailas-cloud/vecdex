package vecdex

import (
	"context"
	"fmt"
)

// Hit is a typed search result.
type Hit[T any] struct {
	Item  T
	Score float64
}

// SearchBuilder is a fluent builder for typed search queries.
type SearchBuilder[T any] struct {
	idx *TypedIndex[T]
	// Text parameters.
	query string
	mode  SearchMode

	// Common parameters.
	filters []FilterCondition
	limit   int
}

// Query sets the text query for semantic/keyword/hybrid search.
func (b *SearchBuilder[T]) Query(q string) *SearchBuilder[T] {
	b.query = q
	return b
}

// Mode sets the search mode (semantic, keyword, hybrid).
func (b *SearchBuilder[T]) Mode(m SearchMode) *SearchBuilder[T] {
	b.mode = m
	return b
}

// Where adds a tag filter condition (exact match).
func (b *SearchBuilder[T]) Where(key, value string) *SearchBuilder[T] {
	b.filters = append(b.filters, FilterCondition{Key: key, Match: value})
	return b
}

// Limit sets the maximum number of results.
func (b *SearchBuilder[T]) Limit(n int) *SearchBuilder[T] {
	b.limit = n
	return b
}

// Do executes the search and returns typed results.
func (b *SearchBuilder[T]) Do(ctx context.Context) ([]Hit[T], error) {
	return b.doText(ctx)
}

func (b *SearchBuilder[T]) doText(ctx context.Context) ([]Hit[T], error) {
	opts := &SearchOptions{
		Mode:  b.mode,
		Limit: b.limit,
	}
	if len(b.filters) > 0 {
		opts.Filters = FilterExpression{Must: b.filters}
	}

	resp, err := b.idx.client.Search(b.idx.name).Query(ctx, b.query, opts)
	if err != nil {
		return nil, fmt.Errorf("text search: %w", err)
	}
	return b.toHits(resp.Results)
}

func (b *SearchBuilder[T]) toHits(results []SearchResult) ([]Hit[T], error) {
	hits := make([]Hit[T], len(results))
	for i, r := range results {
		doc := Document{
			ID:       r.ID,
			Content:  r.Content,
			Tags:     r.Tags,
			Numerics: r.Numerics,
		}
		item, ok := b.idx.meta.fromDocument(doc).(T)
		if !ok {
			return nil, fmt.Errorf(
				"result %d: type assertion to %T failed", i, *new(T),
			)
		}
		hits[i] = Hit[T]{
			Item:  item,
			Score: r.Score,
		}
	}
	return hits, nil
}
