package valkey

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/kailas-cloud/vecdex/internal/db"
	domusage "github.com/kailas-cloud/vecdex/internal/domain/usage"
	"github.com/redis/rueidis"
)

// Receipts outlive the period in which they can authorize a spend. Counter
// retention exceeds receipt retention, so a late completion can still reconcile
// the original day/month without reconstructing missing reservations.
const creditReceiptLifetime = 90 * 24 * time.Hour
const creditCounterLifetime = 93 * 24 * time.Hour
const creditSettledGrantLifetime = 24 * time.Hour

type creditPoolState struct {
	GrantID       string `json:"grant_id"`
	Capacity      int64  `json:"capacity"`
	Available     int64  `json:"available"`
	Pending       int64  `json:"pending"`
	PendingAmount int64  `json:"pending_amount"`
	ExpiresAt     int64  `json:"expires_at"`
	Closed        bool   `json:"closed"`
}

type creditInvocation struct {
	GrantID string `json:"grant_id"`
	Amount  int64  `json:"amount"`
	Done    bool   `json:"done"`
}

type creditPoolTransition struct {
	pool                                 creditPoolState
	receipt                              creditInvocation
	counters                             domusage.Snapshot
	writeCounters, writeReceipt, changed bool
	refund                               int64
}

// CreditPool applies only fixed-size native transactions. An expiring live key
// is watched alongside the persistent pool and receipt, fencing reservations
// whose preparation crosses the lease deadline.
func (s *Store) CreditPool(ctx context.Context, c *db.CreditPoolCommand) (db.CreditPoolResult, error) {
	if err := validateCreditPoolCommand(c); err != nil {
		return db.CreditPoolResult{}, err
	}
	for attempt := 0; attempt < maxTransactionAttempts; attempt++ {
		result, committed, err := s.creditPoolAttempt(ctx, c)
		if !canRetryTransaction(err) && (err != nil || committed) {
			return result, err
		}
		if err := transactionRetry(ctx, attempt); err != nil {
			return result, err
		}
	}
	return db.CreditPoolResult{}, fmt.Errorf("credit pool contention")
}

func validateCreditPoolCommand(c *db.CreditPoolCommand) error {
	if c.GrantID == "" || c.PoolKey == "" || c.LiveKey == "" || len(c.CounterKeys) != 3 {
		return fmt.Errorf("invalid credit pool keys")
	}
	switch c.Action {
	case "install", "close":
		if c.Capacity <= 0 || c.ExpiresAt <= 0 {
			return fmt.Errorf("invalid credit pool allocation")
		}
	case "reserve":
		if c.ReceiptKey == "" || c.Amount <= 0 {
			return fmt.Errorf("invalid credit reservation")
		}
	case "complete", "cancel":
		return validateCreditCompletion(c)
	default:
		return fmt.Errorf("unknown credit pool action")
	}
	return nil
}

func validateCreditCompletion(c *db.CreditPoolCommand) error {
	if c.ReceiptKey == "" || c.Tokens < 0 {
		return fmt.Errorf("invalid credit completion")
	}
	if c.Action == "cancel" && (c.Tokens != 0 || c.Unknown) {
		return fmt.Errorf("invalid credit cancellation")
	}
	return nil
}

func (s *Store) creditPoolAttempt(
	ctx context.Context, c *db.CreditPoolCommand,
) (db.CreditPoolResult, bool, error) {
	client, release := s.client.Dedicate()
	defer release()
	defer unwatch(client)
	keys := append([]string{c.PoolKey, c.LiveKey}, c.CounterKeys...)
	if c.ReceiptKey != "" {
		keys = append(keys, c.ReceiptKey)
	}
	if err := watchKeys(ctx, client, keys...); err != nil {
		return db.CreditPoolResult{}, false, err
	}
	transition, live, err := readCreditPool(ctx, client, c)
	if err != nil {
		consistent, err := validateCreditReadError(ctx, client, err)
		return db.CreditPoolResult{}, consistent, err
	}
	if err := applyCreditPool(c, &transition, live, time.Now().UnixMilli()); err != nil {
		consistent, err := validateCreditReadError(ctx, client, err)
		return db.CreditPoolResult{}, consistent, err
	}
	result := creditPoolResult(&transition)
	if !transition.changed {
		// Validate all watched reads, including idempotent close/complete replies.
		// A concurrently completed invocation can increase the refundable balance.
		_, committed, err := execTransaction(ctx, client, nil)
		return result, committed, err
	}
	commands, err := creditPoolCommands(client, c, &transition)
	if err != nil {
		return result, false, err
	}
	_, committed, err := execTransaction(ctx, client, commands)
	return result, committed, err
}

func readCreditPool(
	ctx context.Context, client rueidis.DedicatedClient, c *db.CreditPoolCommand,
) (creditPoolTransition, bool, error) {
	var result creditPoolTransition
	raw, err := client.Do(ctx, client.B().Get().Key(c.PoolKey).Build()).ToString()
	if err != nil && !rueidis.IsRedisNil(err) {
		return result, false, fmt.Errorf("read credit pool: %w", err)
	}
	if !rueidis.IsRedisNil(err) {
		if err := json.Unmarshal([]byte(raw), &result.pool); err != nil {
			return result, false, fmt.Errorf("decode credit pool: %w", err)
		}
		if err := validateCreditPoolState(&result.pool, c.GrantID); err != nil {
			return result, false, err
		}
	}
	live, err := client.Do(ctx, client.B().Get().Key(c.LiveKey).Build()).ToString()
	if err != nil && !rueidis.IsRedisNil(err) {
		return result, false, fmt.Errorf("read credit lease guard: %w", err)
	}
	if live != "" && live != c.GrantID {
		return result, false, fmt.Errorf("credit lease guard belongs to another grant")
	}
	if err == nil && (live == "" || result.pool.GrantID == "") {
		return result, false, fmt.Errorf("credit lease guard has no valid pool")
	}
	if c.Action == "install" {
		return result, live != "", nil
	}
	counters, receipt, err := readCreditCounters(ctx, client, c)
	result.counters, result.receipt = counters, receipt
	return result, live != "", err
}

func readCreditCounters(
	ctx context.Context, client rueidis.DedicatedClient, c *db.CreditPoolCommand,
) (domusage.Snapshot, creditInvocation, error) {
	commands := make([]rueidis.Completed, 0, 4)
	for _, key := range c.CounterKeys {
		commands = append(commands, client.B().Hgetall().Key(key).Build())
	}
	if c.ReceiptKey != "" {
		commands = append(commands, client.B().Get().Key(c.ReceiptKey).Build())
	}
	replies := client.DoMulti(ctx, commands...)
	if len(replies) != len(commands) {
		return domusage.Snapshot{}, creditInvocation{}, fmt.Errorf("credit ledger reply count mismatch")
	}
	values := make([]rueidis.RedisMessage, 3)
	for i := range values {
		value, err := replies[i].ToMessage()
		if err != nil {
			return domusage.Snapshot{}, creditInvocation{}, fmt.Errorf("read credit usage: %w", err)
		}
		values[i] = value
	}
	snapshot, err := parseBudgetSnapshot(values)
	if err != nil {
		return snapshot, creditInvocation{}, err
	}
	var receipt creditInvocation
	if c.ReceiptKey != "" {
		receipt, err = decodeCreditInvocation(replies[3])
	}
	return snapshot, receipt, err
}

func decodeCreditInvocation(reply rueidis.RedisResult) (creditInvocation, error) {
	var receipt creditInvocation
	raw, err := reply.ToString()
	if rueidis.IsRedisNil(err) {
		return receipt, nil
	}
	if err != nil {
		return receipt, fmt.Errorf("read credit invocation: %w", err)
	}
	if err := json.Unmarshal([]byte(raw), &receipt); err != nil {
		return receipt, fmt.Errorf("decode credit invocation: %w", err)
	}
	if receipt.GrantID == "" {
		return receipt, fmt.Errorf("invalid stored credit invocation")
	}
	return receipt, nil
}

func validateCreditPoolState(pool *creditPoolState, grant string) error {
	if pool.GrantID != grant || pool.Capacity <= 0 || pool.Available < 0 || pool.Available > pool.Capacity ||
		pool.Pending < 0 || pool.PendingAmount < 0 || pool.PendingAmount > pool.Capacity ||
		pool.ExpiresAt <= 0 || pool.Available > pool.Capacity-pool.PendingAmount ||
		pool.Pending > pool.PendingAmount || (pool.Pending == 0 && pool.PendingAmount != 0) {
		return fmt.Errorf("invalid stored credit pool")
	}
	return nil
}

func creditPoolResult(t *creditPoolTransition) db.CreditPoolResult {
	return db.CreditPoolResult{Capacity: t.pool.Capacity, Available: t.pool.Available,
		Pending: t.pool.Pending, PendingAmount: t.pool.PendingAmount, Closed: t.pool.Closed,
		Applied: t.changed, Refund: t.refund}
}

func creditPoolCommands(
	client rueidis.DedicatedClient, c *db.CreditPoolCommand, t *creditPoolTransition,
) ([]rueidis.Completed, error) {
	encoded, err := json.Marshal(&t.pool)
	if err != nil {
		return nil, fmt.Errorf("encode credit pool: %w", err)
	}
	commands := []rueidis.Completed{
		client.B().Set().Key(c.PoolKey).Value(string(encoded)).Px(creditReceiptLifetime).Build(),
	}
	if t.pool.Closed {
		commands = append(commands, client.B().Unlink().Key(c.LiveKey).Build())
	} else if c.Action == "install" {
		commands = append(commands, client.B().Arbitrary("SET").Keys(c.LiveKey).
			Args(c.GrantID, "PXAT", strconv.FormatInt(t.pool.ExpiresAt, 10)).Build())
	}
	if t.writeCounters {
		commands = append(commands, creditCounterCommands(client, c.CounterKeys, &t.counters)...)
	}
	if t.writeReceipt {
		receipt, err := json.Marshal(&t.receipt)
		if err != nil {
			return nil, fmt.Errorf("encode credit invocation: %w", err)
		}
		commands = append(commands,
			client.B().Set().Key(c.ReceiptKey).Value(string(receipt)).Px(creditReceiptLifetime).Build())
	}
	return commands, nil
}

func creditCounterCommands(
	client rueidis.DedicatedClient, keys []string, snapshot *domusage.Snapshot,
) []rueidis.Completed {
	commands := make([]rueidis.Completed, 0, 5)
	for i, counter := range []domusage.Counters{snapshot.Day, snapshot.Month, snapshot.Total} {
		commands = append(commands, client.B().Hset().Key(keys[i]).FieldValue().
			FieldValue("tokens", strconv.FormatInt(counter.Tokens, 10)).
			FieldValue("reserved", strconv.FormatInt(counter.Reserved, 10)).
			FieldValue("requests", strconv.FormatInt(counter.Requests, 10)).
			FieldValue("unknown", strconv.FormatInt(counter.Unknown, 10)).Build())
	}
	return append(commands,
		client.B().Pexpire().Key(keys[0]).Milliseconds(creditCounterLifetime.Milliseconds()).Build(),
		client.B().Pexpire().Key(keys[1]).Milliseconds(creditCounterLifetime.Milliseconds()).Build())
}
