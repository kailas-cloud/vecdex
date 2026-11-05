package valkey

import (
	"context"
	"errors"
	"fmt"

	"github.com/redis/rueidis"
)

// WATCH does not make separate reads atomic. Before reporting an invalid
// transition, prove the reads still describe one version. Empty EXEC writes
// nothing: a conflict requests a fresh attempt, while a stable error survives.
func validateCreditReadError(ctx context.Context, client rueidis.DedicatedClient, readErr error) (bool, error) {
	_, consistent, err := execTransaction(ctx, client, nil)
	if err != nil {
		return false, errors.Join(readErr, fmt.Errorf("validate credit reads: %w", err))
	}
	if !consistent {
		return false, nil
	}
	return true, readErr
}
