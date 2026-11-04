package domain

import "context"

type expectedRevisionKey struct{}

// WithExpectedRevision carries an optional conditional-write requirement to storage.
func WithExpectedRevision(ctx context.Context, revision *int) context.Context {
	if revision == nil {
		return ctx
	}
	return context.WithValue(ctx, expectedRevisionKey{}, *revision)
}

// ExpectedRevision returns the revision required for an atomic write, if supplied.
func ExpectedRevision(ctx context.Context) *int {
	value, ok := ctx.Value(expectedRevisionKey{}).(int)
	if !ok {
		return nil
	}
	return &value
}

type collectionGenerationKey struct{}

// WithCollectionGeneration binds an operation to the collection instance it read.
func WithCollectionGeneration(ctx context.Context, generation string) context.Context {
	return context.WithValue(ctx, collectionGenerationKey{}, generation)
}

// CollectionGeneration returns the immutable identity of the collection instance.
func CollectionGeneration(ctx context.Context) string {
	value, _ := ctx.Value(collectionGenerationKey{}).(string)
	return value
}

type excludedDocumentKey struct{}

// WithExcludedDocument removes every fragment of the source document from similarity retrieval.
func WithExcludedDocument(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, excludedDocumentKey{}, id)
}

// ExcludedDocument returns the logical document excluded from retrieval.
func ExcludedDocument(ctx context.Context) string {
	id, _ := ctx.Value(excludedDocumentKey{}).(string)
	return id
}
