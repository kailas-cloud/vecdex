package domain

import (
	"context"
	"fmt"
)

type expectedCollectionGenerationKey struct{}
type requireAbsentKey struct{}

// WithExpectedCollectionGeneration fences public operations to one collection
// incarnation. It is separate from the resolved storage-routing generation.
func WithExpectedCollectionGeneration(ctx context.Context, generation string) context.Context {
	return context.WithValue(ctx, expectedCollectionGenerationKey{}, generation)
}

// ExpectedCollectionGeneration returns the caller's optional collection fence.
func ExpectedCollectionGeneration(ctx context.Context) string {
	generation, _ := ctx.Value(expectedCollectionGenerationKey{}).(string)
	return generation
}

// ValidateCollectionGeneration rejects a recreated collection before doing work.
func ValidateCollectionGeneration(ctx context.Context, actual string) error {
	if expected := ExpectedCollectionGeneration(ctx); expected != "" && expected != actual {
		return fmt.Errorf("collection incarnation changed: %w", ErrCollectionGenerationMismatch)
	}
	return nil
}

// WithRequireAbsent makes an upsert create-only. False clears an inherited condition.
func WithRequireAbsent(ctx context.Context, absent bool) context.Context {
	return context.WithValue(ctx, requireAbsentKey{}, absent)
}

// RequireAbsent reports whether an upsert must not replace a live source.
func RequireAbsent(ctx context.Context) bool {
	absent, _ := ctx.Value(requireAbsentKey{}).(bool)
	return absent
}
