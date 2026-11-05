package chi

import (
	"context"
	"fmt"
	"strings"

	"github.com/kailas-cloud/vecdex/internal/domain"
)

func idempotencyContext(ctx context.Context, key *string) (context.Context, error) {
	if key == nil {
		return ctx, nil
	}
	if err := domain.ValidateIdempotencyKey(*key); err != nil {
		return ctx, fmt.Errorf("Idempotency-Key: %w", err)
	}
	return domain.WithIdempotencyKey(ctx, *key), nil
}

// expectedVersion accepts one quoted strong document ETag. Numeric revisions
// cannot distinguish a deleted source from another source with the same ID.
func expectedVersion(header *string) (*string, error) {
	if header == nil {
		return nil, nil //nolint:nilnil // No header intentionally means an unconditional write.
	}
	value := strings.TrimSpace(*header)
	if len(value) < 3 || value[0] != '"' || value[len(value)-1] != '"' {
		return nil, fmt.Errorf("If-Match requires one quoted document ETag: %w", domain.ErrInvalidSchema)
	}
	value = value[1 : len(value)-1]
	for _, char := range value {
		if char <= ' ' || char == 127 || char == '"' || char == ',' {
			return nil, fmt.Errorf("invalid document ETag: %w", domain.ErrInvalidSchema)
		}
	}
	return &value, nil
}
