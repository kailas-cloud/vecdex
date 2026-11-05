package domain

import (
	"context"
	"fmt"
)

type idempotencyKeyContext struct{}
type writeClaimContext struct{}

// WriteClaim identifies the partition-local reservation owned by one attempt.
type WriteClaim struct{ Key, Owner, Fingerprint string }

// ValidateIdempotencyKey accepts opaque printable ASCII keys up to 128 bytes.
func ValidateIdempotencyKey(key string) error {
	if len(key) < 1 || len(key) > 128 {
		return fmt.Errorf("idempotency key must contain 1–128 bytes: %w", ErrInvalidSchema)
	}
	for _, char := range key {
		if char < 33 || char > 126 {
			return fmt.Errorf("idempotency key must use printable ASCII without spaces: %w", ErrInvalidSchema)
		}
	}
	return nil
}

// WithIdempotencyKey binds a source operation; empty clears an inherited key.
func WithIdempotencyKey(ctx context.Context, key string) context.Context {
	return context.WithValue(ctx, idempotencyKeyContext{}, key)
}

// IdempotencyKey returns the caller's optional source operation key.
func IdempotencyKey(ctx context.Context) string {
	key, _ := ctx.Value(idempotencyKeyContext{}).(string)
	return key
}

// WithWriteClaim carries the native claim to the atomic source commit.
func WithWriteClaim(ctx context.Context, claim WriteClaim) context.Context {
	return context.WithValue(ctx, writeClaimContext{}, claim)
}

// CurrentWriteClaim returns the reservation acquired before embedding.
func CurrentWriteClaim(ctx context.Context) (WriteClaim, bool) {
	claim, ok := ctx.Value(writeClaimContext{}).(WriteClaim)
	return claim, ok
}
