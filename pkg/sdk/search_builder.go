package vecdex

import (
	"context"
	"fmt"
)

// Hit is a typed search result.
type Hit[T any] struct {
	Item     T
	Score    float64
	Distance float64 // meters (geo only, 0 for text)
}

// SearchBuilder is a fluent builder for typed search queries.
type SearchBuilder[T any] struct {
	idx *TypedIndex[T]

	// Geo parameters.
	lat, lon float64
	radiusKm float64

	// Text parameters.
	query string
	mode  SearchMode

	// Common parameters.
	filters []FilterCondition
	limit   int
}

// Near sets the geographic center point for geo search.
func (b *SearchBuilder[T]) Near(lat, lon float64) *SearchBuilder[T] {
	b.lat = lat
	b.lon = lon
	return b
}

// Km sets the search radius in kilometers for geo search.
// Converted to minScore (meters) internally.
func (b *SearchBuilder[T]) Km(radius float64) *SearchBuilder[T] {
	b.radiusKm = radius
	return b
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
	if b.idx.meta.colType == CollectionTypeGeo {
		return b.doGeo(ctx)
	}
	return b.doText(ctx)
}

func (b *SearchBuilder[T]) doGeo(ctx context.Context) ([]Hit[T], error) {
	topK := b.limit
	if topK == 0 {
		topK = 10
	}

	var opts SearchOptions
	if len(b.filters) > 0 {
		opts.Filters = FilterExpression{Must: b.filters}
	}
	if b.radiusKm > 0 {
		opts.MinScore = b.radiusKm * 1000 // km → meters
	}
	opts.Limit = b.limit

	results, err := b.idx.client.Search(b.idx.name).Geo(
		ctx, b.lat, b.lon, topK, opts,
	)
	if err != nil {
		return nil, fmt.Errorf("geo search: %w", err)
	}
	return b.toHits(results, true), nil
}

func (b *SearchBuilder[T]) doText(ctx context.Context) ([]Hit[T], error) {
	opts := &SearchOptions{
		Mode:  b.mode,
		Limit: b.limit,
	}
	if len(b.filters) > 0 {
		opts.Filters = FilterExpression{Must: b.filters}
	}

	results, err := b.idx.client.Search(b.idx.name).Query(ctx, b.query, opts)
	if err != nil {
		return nil, fmt.Errorf("text search: %w", err)
	}
	return b.toHits(results, false), nil
}

func (b *SearchBuilder[T]) toHits(results []SearchResult, isGeo bool) []Hit[T] {
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
			continue
		}
		hits[i] = Hit[T]{
			Item:  item,
			Score: r.Score,
		}
		if isGeo {
			hits[i].Distance = r.Score
		}
	}
	return hits
}
