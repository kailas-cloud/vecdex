package vecdex

import (
	"context"
	"fmt"
)

// TypedIndex is a generic, schema-first index backed by a vecdex Client.
// Schema is inferred from T's struct tags at construction time.
type TypedIndex[T any] struct {
	name   string
	client *Client
	meta   *schemaMeta
}

// NewIndex creates a typed index handle for the given collection name.
// T must be a struct with vecdex tags. Schema is parsed once and cached.
func NewIndex[T any](client *Client, name string) (*TypedIndex[T], error) {
	meta, err := parseSchema[T]()
	if err != nil {
		return nil, fmt.Errorf("new index %q: %w", name, err)
	}
	return &TypedIndex[T]{name: name, client: client, meta: meta}, nil
}

// Ensure creates the collection if it does not exist (idempotent).
func (idx *TypedIndex[T]) Ensure(ctx context.Context) error {
	_, err := idx.client.Collections().Ensure(ctx, idx.name, idx.meta.collectionOptions()...)
	if err != nil {
		return fmt.Errorf("ensure %q: %w", idx.name, err)
	}
	return nil
}

// Upsert creates or updates a single item and returns its committed source version.
func (idx *TypedIndex[T]) Upsert(ctx context.Context, item T, options ...WriteOption) (WriteResult, error) {
	doc := idx.meta.toDocument(item)
	return idx.client.Documents(idx.name).Upsert(ctx, doc, options...)
}

// UpsertBatch creates or updates items in batch.
func (idx *TypedIndex[T]) UpsertBatch(
	ctx context.Context, items []T,
) (BatchResponse, error) {
	docs := make([]BatchUpsertItem, len(items))
	for i, item := range items {
		doc := idx.meta.toDocument(item)
		docs[i] = BatchUpsertItem{ID: doc.ID, Content: doc.Content, Metadata: doc.Metadata}
	}
	return idx.client.Documents(idx.name).BatchUpsert(ctx, docs)
}

// Get retrieves a typed item by ID.
func (idx *TypedIndex[T]) Get(ctx context.Context, id string) (T, error) {
	doc, err := idx.client.Documents(idx.name).Get(ctx, id)
	if err != nil {
		var zero T
		return zero, fmt.Errorf("get: %w", err)
	}
	decoded, err := idx.meta.fromDocument(doc)
	if err != nil {
		var zero T
		return zero, fmt.Errorf("get: %w", err)
	}
	item, ok := decoded.(T)
	if !ok {
		var zero T
		return zero, fmt.Errorf("get: type assertion failed")
	}
	return item, nil
}

// Delete removes an item by ID.
func (idx *TypedIndex[T]) Delete(ctx context.Context, id string, options ...WriteOption) error {
	return idx.client.Documents(idx.name).Delete(ctx, id, options...)
}

// Count returns the number of items in the collection.
func (idx *TypedIndex[T]) Count(ctx context.Context) (int, error) {
	return idx.client.Documents(idx.name).Count(ctx)
}

// Search returns a fluent search builder for this index.
func (idx *TypedIndex[T]) Search() *SearchBuilder[T] {
	return &SearchBuilder[T]{idx: idx}
}
