package valkey

import (
	"context"
	"fmt"
	"math"
	"strconv"

	"github.com/kailas-cloud/vecdex/internal/db"
	"github.com/kailas-cloud/vecdex/internal/domain"
	domusage "github.com/kailas-cloud/vecdex/internal/domain/usage"
	"github.com/redis/rueidis"
)

// Budget uses optimistic native transactions over a fixed set of four keys.
// All accounting, validation and retry logic runs in Go, without Lua.
func (s *Store) Budget(ctx context.Context, c *db.BudgetCommand) (domusage.Snapshot, error) {
	if err := validateBudgetCommand(c); err != nil {
		return domusage.Snapshot{}, err
	}
	return s.retryBudget(ctx, c)
}

func validateBudgetCommand(c *db.BudgetCommand) error {
	if len(c.Keys) != 4 {
		return fmt.Errorf("budget requires four keys")
	}
	if c.Action != "read" && c.Action != "reserve" && c.Action != "complete" {
		return fmt.Errorf("invalid budget action")
	}
	if c.Amount < 0 || c.Tokens < 0 {
		return fmt.Errorf("negative budget amount")
	}
	return nil
}

func (s *Store) retryBudget(ctx context.Context, c *db.BudgetCommand) (domusage.Snapshot, error) {
	for attempt := 0; attempt < maxTransactionAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return domusage.Snapshot{}, fmt.Errorf("budget operation canceled: %w", err)
		}
		snapshot, committed, err := s.budgetAttempt(ctx, c)
		if err != nil || committed {
			return snapshot, err
		}
		if err := transactionRetry(ctx, attempt); err != nil {
			return snapshot, err
		}
	}
	return domusage.Snapshot{}, fmt.Errorf("budget transaction contention")
}

func (s *Store) budgetAttempt(ctx context.Context, c *db.BudgetCommand) (domusage.Snapshot, bool, error) {
	client, release := s.client.Dedicate()
	defer release()
	defer unwatch(client)
	if err := watchKeys(ctx, client, c.Keys...); err != nil {
		return domusage.Snapshot{}, false, err
	}
	if c.Action == "read" {
		return readBudgetTransaction(ctx, client, c.Keys[:3])
	}
	snapshot, receipt, err := readBudgetState(ctx, client, c.Keys)
	if err != nil {
		return snapshot, false, err
	}
	changed, err := applyBudget(c, &snapshot, receipt)
	if err != nil {
		return snapshot, false, err
	}
	if !changed {
		return readBudgetTransaction(ctx, client, c.Keys[:3])
	}
	commands := budgetCommands(client, c, &snapshot)
	_, committed, err := execTransaction(ctx, client, commands)
	return snapshot, committed, err
}

func readBudgetTransaction(
	ctx context.Context,
	client rueidis.DedicatedClient,
	keys []string,
) (domusage.Snapshot, bool, error) {
	commands := make([]rueidis.Completed, len(keys))
	for i, key := range keys {
		commands[i] = client.B().Hgetall().Key(key).Build()
	}
	values, committed, err := execTransaction(ctx, client, commands)
	if err != nil || !committed {
		return domusage.Snapshot{}, committed, err
	}
	snapshot, err := parseBudgetSnapshot(values)
	return snapshot, true, err
}

func readBudgetState(
	ctx context.Context,
	client rueidis.DedicatedClient,
	keys []string,
) (domusage.Snapshot, string, error) {
	commands := make([]rueidis.Completed, 0, 4)
	for _, key := range keys[:3] {
		commands = append(commands, client.B().Hgetall().Key(key).Build())
	}
	commands = append(commands, client.B().Get().Key(keys[3]).Build())
	replies := client.DoMulti(ctx, commands...)
	if len(replies) != 4 {
		return domusage.Snapshot{}, "", fmt.Errorf("incomplete budget read")
	}
	values := make([]rueidis.RedisMessage, 3)
	for i := range values {
		value, err := replies[i].ToMessage()
		if err != nil {
			return domusage.Snapshot{}, "", fmt.Errorf("read budget counters: %w", err)
		}
		values[i] = value
	}
	snapshot, err := parseBudgetSnapshot(values)
	if err != nil {
		return snapshot, "", err
	}
	receipt, err := replies[3].ToString()
	if err != nil && !rueidis.IsRedisNil(err) {
		return snapshot, "", fmt.Errorf("read budget receipt: %w", err)
	}
	return snapshot, receipt, nil
}

func parseBudgetSnapshot(values []rueidis.RedisMessage) (domusage.Snapshot, error) {
	if len(values) != 3 {
		return domusage.Snapshot{}, fmt.Errorf("invalid budget snapshot")
	}
	counters := make([]domusage.Counters, 3)
	for i, value := range values {
		fields, err := value.AsStrMap()
		if err != nil {
			return domusage.Snapshot{}, fmt.Errorf("decode budget hash: %w", err)
		}
		numbers := make([]int64, 4)
		for j, name := range []string{"tokens", "reserved", "requests", "unknown"} {
			if fields[name] == "" {
				continue
			}
			numbers[j], err = strconv.ParseInt(fields[name], 10, 64)
			if err != nil || numbers[j] < 0 {
				return domusage.Snapshot{}, fmt.Errorf("invalid budget counter %s", name)
			}
		}
		counters[i] = domusage.Counters{
			Tokens:   numbers[0],
			Reserved: numbers[1],
			Requests: numbers[2],
			Unknown:  numbers[3],
		}
	}
	return domusage.Snapshot{Day: counters[0], Month: counters[1], Total: counters[2]}, nil
}

func applyBudget(c *db.BudgetCommand, snapshot *domusage.Snapshot, receipt string) (bool, error) {
	if c.Action == "reserve" {
		return reserveBudget(c, snapshot, receipt)
	}
	if receipt == "" || receipt == "done" {
		return false, nil
	}
	amount, err := strconv.ParseInt(receipt, 10, 64)
	if err != nil || amount < 0 {
		return false, fmt.Errorf("invalid budget receipt")
	}
	for _, counter := range []*domusage.Counters{&snapshot.Day, &snapshot.Month, &snapshot.Total} {
		if err := completeBudgetCounters(counter, c, amount); err != nil {
			return false, err
		}
	}
	return true, nil
}

func reserveBudget(c *db.BudgetCommand, snapshot *domusage.Snapshot, receipt string) (bool, error) {
	if receipt != "" {
		return false, nil
	}
	if !budgetFits(&snapshot.Day, c.DailyLimit, c.Amount) ||
		!budgetFits(&snapshot.Month, c.MonthlyLimit, c.Amount) {
		return false, domain.ErrEmbeddingQuotaExceeded
	}
	for _, counter := range []*domusage.Counters{&snapshot.Day, &snapshot.Month, &snapshot.Total} {
		if c.Amount > math.MaxInt64-counter.Reserved {
			return false, fmt.Errorf("budget reservation overflow")
		}
		counter.Reserved += c.Amount
	}
	return true, nil
}

func budgetFits(counter *domusage.Counters, limit, amount int64) bool {
	if limit <= 0 {
		return true
	}
	if counter.Tokens > limit {
		return false
	}
	available := limit - counter.Tokens
	return counter.Reserved <= available && amount <= available-counter.Reserved
}

func completeBudgetCounters(counter *domusage.Counters, c *db.BudgetCommand, amount int64) error {
	if c.Tokens > math.MaxInt64-counter.Tokens || counter.Requests == math.MaxInt64 ||
		(c.Unknown && counter.Unknown == math.MaxInt64) {
		return fmt.Errorf("budget counter overflow")
	}
	if !c.Unknown && counter.Reserved < amount {
		return fmt.Errorf("budget reservation is missing")
	}
	counter.Tokens += c.Tokens
	counter.Requests++
	if c.Unknown {
		counter.Unknown++
	} else {
		counter.Reserved -= amount
	}
	return nil
}

func budgetCommands(
	client rueidis.DedicatedClient,
	c *db.BudgetCommand,
	snapshot *domusage.Snapshot,
) []rueidis.Completed {
	commands := make([]rueidis.Completed, 0, 6)
	for i, counter := range []domusage.Counters{snapshot.Day, snapshot.Month, snapshot.Total} {
		commands = append(commands, client.B().Hset().Key(c.Keys[i]).FieldValue().
			FieldValue("tokens", strconv.FormatInt(counter.Tokens, 10)).
			FieldValue("reserved", strconv.FormatInt(counter.Reserved, 10)).
			FieldValue("requests", strconv.FormatInt(counter.Requests, 10)).
			FieldValue("unknown", strconv.FormatInt(counter.Unknown, 10)).Build())
	}
	receipt, ttl := strconv.FormatInt(c.Amount, 10), int64(5356800)
	if c.Action == "complete" {
		receipt, ttl = "done", 86400
	}
	commands = append(commands, client.B().Set().Key(c.Keys[3]).Value(receipt).ExSeconds(ttl).Build(),
		client.B().Expire().Key(c.Keys[0]).Seconds(172800).Nx().Build(),
		client.B().Expire().Key(c.Keys[1]).Seconds(5356800).Nx().Build())
	return commands
}
