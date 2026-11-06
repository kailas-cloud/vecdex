package vecdex

import (
	"context"
	"fmt"

	"github.com/kailas-cloud/vecdex/internal/domain"
)

// WriteOption adds a condition to a source-document write.
type WriteOption interface{ applyWrite(*writeCondition) }
type writeOptionFunc func(*writeCondition)

func (option writeOptionFunc) applyWrite(condition *writeCondition) { option(condition) }

type writeCondition struct {
	version, idempotencyKey *string
	absent                  bool
}

// IfAbsent makes Upsert create-only, including when an old source has expired.
// It cannot be combined with IfVersion or used for Patch or Delete.
func IfAbsent() WriteOption {
	return writeOptionFunc(func(condition *writeCondition) { condition.absent = true })
}

// IfVersion requires the exact token returned by Get, List or search. It protects
// against concurrent updates and deletion/recreation of the same document ID.
func IfVersion(version string) WriteOption {
	return writeOptionFunc(func(condition *writeCondition) { condition.version = &version })
}

// IdempotencyKey replays a successful source operation for 24 hours. Reusing
// the key for different input on the same source returns ErrIdempotencyConflict.
func IdempotencyKey(key string) WriteOption {
	return writeOptionFunc(func(condition *writeCondition) { condition.idempotencyKey = &key })
}

func applyWriteOptions(ctx context.Context, options []WriteOption) (context.Context, error) {
	var condition writeCondition
	for _, option := range options {
		option.applyWrite(&condition)
	}
	if err := domain.ValidateExpectedVersion(condition.version); err != nil {
		return ctx, fmt.Errorf("write condition: %w", err)
	}
	if condition.absent && condition.version != nil {
		return ctx, fmt.Errorf("IfAbsent and IfVersion are mutually exclusive: %w", domain.ErrInvalidSchema)
	}
	key := ""
	if condition.idempotencyKey != nil {
		key = *condition.idempotencyKey
		if err := domain.ValidateIdempotencyKey(key); err != nil {
			return ctx, fmt.Errorf("write request key: %w", err)
		}
	}
	ctx = domain.WithIdempotencyKey(domain.WithExpectedVersion(ctx, condition.version), key)
	return domain.WithRequireAbsent(ctx, condition.absent), nil
}
