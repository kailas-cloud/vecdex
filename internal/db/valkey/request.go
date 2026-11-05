package valkey

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/kailas-cloud/vecdex/internal/db"
	"github.com/kailas-cloud/vecdex/internal/domain"
	"github.com/redis/rueidis"
)

const requestClaimLifetime = 10 * time.Minute
const requestResultCapacity = 512

// ClaimWriteRequest acquires an expiring source claim or replays a 24-hour receipt.
func (s *Store) ClaimWriteRequest(ctx context.Context, request *db.WriteRequest) (db.WriteRequestResult, error) {
	if request.Key == "" || request.Owner == "" || request.Fingerprint == "" {
		return db.WriteRequestResult{}, fmt.Errorf("invalid idempotency claim")
	}
	for attempt := 0; attempt < maxTransactionAttempts; attempt++ {
		result, committed, err := s.claimWriteRequest(ctx, request)
		if !canRetryTransaction(err) && (err != nil || committed) {
			return result, err
		}
		if err := transactionRetry(ctx, attempt); err != nil {
			return result, err
		}
	}
	return db.WriteRequestResult{}, domain.ErrIdempotencyInProgress
}

func (s *Store) claimWriteRequest(
	ctx context.Context, request *db.WriteRequest,
) (db.WriteRequestResult, bool, error) {
	client, release := s.client.Dedicate()
	defer release()
	defer unwatch(client)
	keys := []string{request.Key}
	if request.GuardKey != "" {
		keys = append(keys, request.GuardKey)
	}
	if err := watchKeys(ctx, client, keys...); err != nil {
		return db.WriteRequestResult{}, false, err
	}
	guard := &db.HashMutation{GuardKey: request.GuardKey, GuardGeneration: request.GuardGeneration, GuardState: "active"}
	if err := checkMutationGuard(ctx, client, guard); err != nil {
		return db.WriteRequestResult{}, false, err
	}
	fields, err := client.Do(ctx, client.B().Hgetall().Key(request.Key).Build()).AsStrMap()
	if err != nil {
		return db.WriteRequestResult{}, false, fmt.Errorf("read idempotency claim: %w", err)
	}
	if len(fields) != 0 {
		result, err := inspectWriteRequest(request, fields)
		return result, true, err
	}
	// Allocate the bounded result field before document preparation. A commit
	// replaces this padding with a smaller receipt instead of growing the hash.
	_, committed, err := execTransaction(ctx, client, []rueidis.Completed{
		client.B().Hset().Key(request.Key).FieldValue().FieldValue("fingerprint", request.Fingerprint).
			FieldValue("owner", request.Owner).FieldValue("state", "preparing").
			FieldValue("result", strings.Repeat(" ", requestResultCapacity)).Build(),
		client.B().Pexpire().Key(request.Key).Milliseconds(claimLifetime(ctx).Milliseconds()).Build(),
	})
	return db.WriteRequestResult{}, committed, err
}

func claimLifetime(ctx context.Context) time.Duration {
	if deadline, ok := ctx.Deadline(); ok {
		return min(requestClaimLifetime, max(time.Second, time.Until(deadline)+30*time.Second))
	}
	return requestClaimLifetime
}

func inspectWriteRequest(request *db.WriteRequest, fields map[string]string) (db.WriteRequestResult, error) {
	if fields["fingerprint"] != request.Fingerprint {
		return db.WriteRequestResult{}, domain.ErrIdempotencyConflict
	}
	switch fields["state"] {
	case "complete":
		var result db.HashMutationResult
		if err := json.Unmarshal([]byte(fields["result"]), &result); err != nil {
			return db.WriteRequestResult{}, fmt.Errorf("decode idempotency receipt: %w", err)
		}
		return db.WriteRequestResult{Replay: true, Result: result}, nil
	case "preparing":
		if fields["owner"] != request.Owner {
			return db.WriteRequestResult{}, domain.ErrIdempotencyInProgress
		}
		return db.WriteRequestResult{}, nil
	default:
		return db.WriteRequestResult{}, fmt.Errorf("invalid idempotency state")
	}
}

// ReleaseWriteRequest fences an unfinished owner without removing a completed receipt.
func (s *Store) ReleaseWriteRequest(ctx context.Context, request *db.WriteRequest) error {
	for attempt := 0; attempt < maxTransactionAttempts; attempt++ {
		committed, err := s.releaseWriteRequest(ctx, request)
		if !canRetryTransaction(err) && (err != nil || committed) {
			return err
		}
		if err := transactionRetry(ctx, attempt); err != nil {
			return err
		}
	}
	return fmt.Errorf("idempotency release contention")
}

func (s *Store) releaseWriteRequest(ctx context.Context, request *db.WriteRequest) (bool, error) {
	client, release := s.client.Dedicate()
	defer release()
	defer unwatch(client)
	if err := watchKeys(ctx, client, request.Key); err != nil {
		return false, err
	}
	fields, err := client.Do(ctx, client.B().Hgetall().Key(request.Key).Build()).AsStrMap()
	if err != nil {
		return false, fmt.Errorf("read releasing idempotency claim: %w", err)
	}
	if fields["owner"] != request.Owner || fields["state"] != "preparing" {
		return true, nil
	}
	_, committed, err := execTransaction(ctx, client, []rueidis.Completed{client.B().Del().Key(request.Key).Build()})
	return committed, err
}

func checkWriteRequest(
	ctx context.Context, client rueidis.DedicatedClient, request *db.WriteRequest,
) (db.WriteRequestResult, error) {
	fields, err := client.Do(ctx, client.B().Hgetall().Key(request.Key).Build()).AsStrMap()
	if err != nil {
		return db.WriteRequestResult{}, fmt.Errorf("read committing idempotency claim: %w", err)
	}
	if len(fields) == 0 {
		return db.WriteRequestResult{}, domain.ErrIdempotencyInProgress
	}
	return inspectWriteRequest(request, fields)
}

func requestCommitCommands(
	client rueidis.DedicatedClient, request *db.WriteRequest, result db.HashMutationResult,
) ([]rueidis.Completed, error) {
	encoded, err := json.Marshal(result)
	if err != nil {
		return nil, fmt.Errorf("encode idempotency receipt: %w", err)
	}
	if len(encoded) > requestResultCapacity {
		return nil, errors.New("idempotency result exceeds its reserved capacity")
	}
	return []rueidis.Completed{
		client.B().Hset().Key(request.Key).FieldValue().FieldValue("state", "complete").
			FieldValue("result", string(encoded)).Build(),
		client.B().Pexpire().Key(request.Key).Milliseconds(mutationReceiptLifetime.Milliseconds()).Build(),
	}, nil
}
