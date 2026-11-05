package valkey

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/kailas-cloud/vecdex/internal/db"
	"github.com/redis/rueidis"
)

type creditAllocationPeriod struct {
	Booked, Limit, Baseline, UnknownBaseline int64
	present                                  bool
}

type creditGrantTransition struct {
	day, month     creditAllocationPeriod
	result         db.CreditGrantResult
	found, changed bool
}

// CreditGrant reserves bounded portions in the shared day/month ledger. Normal
// invocations spend only their local pools; this slot sees allocation/refund work.
func (s *Store) CreditGrant(ctx context.Context, c *db.CreditGrantCommand) (db.CreditGrantResult, error) {
	if err := validateCreditGrantCommand(c); err != nil {
		return db.CreditGrantResult{}, err
	}
	if c.Action == "read" {
		return readCreditGrant(ctx, s.client, c.GrantKey, c.Grant.ID)
	}
	for attempt := 0; attempt < maxTransactionAttempts; attempt++ {
		result, committed, err := s.creditGrantAttempt(ctx, c)
		if !canRetryTransaction(err) && (err != nil || committed) {
			return result, err
		}
		if err := transactionRetry(ctx, attempt); err != nil {
			return result, err
		}
	}
	return db.CreditGrantResult{}, fmt.Errorf("credit allocation contention")
}

func validateCreditGrantCommand(c *db.CreditGrantCommand) error {
	if c.GrantKey == "" || c.Grant.ID == "" {
		return fmt.Errorf("invalid credit grant key")
	}
	if c.Action == "read" {
		return nil
	}
	if c.DayKey == "" || c.MonthKey == "" || c.ActiveKey == "" || c.DueKey == "" {
		return fmt.Errorf("invalid credit allocation keys")
	}
	switch c.Action {
	case "activate":
		return nil
	case "refund":
		return validateCreditRefund(c)
	case "initialize":
		return validateCreditHistory(c)
	case "grant":
		return validateNewCreditGrant(c)
	default:
		return fmt.Errorf("unknown credit grant action")
	}
}

func validateCreditRefund(c *db.CreditGrantCommand) error {
	if c.Refund < 0 || (c.KeepDue && c.NextCheck <= 0) {
		return fmt.Errorf("invalid credit refund")
	}
	return nil
}

func validateNewCreditGrant(c *db.CreditGrantCommand) error {
	if c.Minimum <= 0 || c.Wanted < c.Minimum ||
		c.Grant.At <= 0 || c.Grant.ExpiresAt <= c.Grant.At {
		return fmt.Errorf("invalid credit allocation")
	}
	if err := validateCreditHistory(c); err != nil {
		return err
	}
	return validateCreditPartitions(c.Partitions)
}

func validateCreditHistory(c *db.CreditGrantCommand) error {
	if c.DailyLimit < 0 || c.MonthlyLimit < 0 || (c.DailyLimit == 0 && c.MonthlyLimit == 0) ||
		c.BaselineDay < 0 || c.BaselineMonth < 0 || c.UnknownDay < 0 || c.UnknownMonth < 0 {
		return fmt.Errorf("invalid credit history baseline")
	}
	return nil
}

func validateCreditPartitions(partitions []int) error {
	if len(partitions) == 0 || len(partitions) > 8 {
		return fmt.Errorf("invalid credit slice count")
	}
	seen := make(map[int]bool, len(partitions))
	for _, partition := range partitions {
		if partition < 0 || partition > 255 || seen[partition] {
			return fmt.Errorf("invalid credit partition")
		}
		seen[partition] = true
	}
	return nil
}

func (s *Store) creditGrantAttempt(
	ctx context.Context, c *db.CreditGrantCommand,
) (db.CreditGrantResult, bool, error) {
	client, release := s.client.Dedicate()
	defer release()
	defer unwatch(client)
	if err := watchKeys(ctx, client, c.DayKey, c.MonthKey, c.GrantKey, c.ActiveKey, c.DueKey); err != nil {
		return db.CreditGrantResult{}, false, err
	}
	state, err := readCreditGrantState(ctx, client, c)
	if err != nil {
		consistent, err := validateCreditReadError(ctx, client, err)
		return db.CreditGrantResult{}, consistent, err
	}
	if err := applyCreditGrant(c, &state, time.Now().UnixMilli()); err != nil {
		consistent, err := validateCreditReadError(ctx, client, err)
		return db.CreditGrantResult{}, consistent, err
	}
	var commands []rueidis.Completed
	if state.changed {
		commands, err = creditGrantCommands(client, c, &state)
		if err != nil {
			return db.CreditGrantResult{}, false, err
		}
	}
	_, committed, err := execTransaction(ctx, client, commands)
	return state.result, committed, err
}

func readCreditGrantState(
	ctx context.Context, client rueidis.DedicatedClient, c *db.CreditGrantCommand,
) (creditGrantTransition, error) {
	var result creditGrantTransition
	for _, key := range []string{c.ActiveKey, c.DueKey} {
		if err := checkCatalogType(ctx, client, key); err != nil {
			return result, err
		}
	}
	commands := []rueidis.Completed{
		client.B().Hgetall().Key(c.DayKey).Build(),
		client.B().Hgetall().Key(c.MonthKey).Build(),
		client.B().Hgetall().Key(c.GrantKey).Build(),
	}
	replies := client.DoMulti(ctx, commands...)
	if len(replies) != 3 {
		return result, fmt.Errorf("credit allocation reply count mismatch")
	}
	periods := []*creditAllocationPeriod{&result.day, &result.month}
	for i, period := range periods {
		fields, err := replies[i].AsStrMap()
		if err != nil {
			return result, fmt.Errorf("read credit allocation period: %w", err)
		}
		*period, err = decodeCreditAllocationPeriod(fields)
		if err != nil {
			return result, err
		}
	}
	fields, err := replies[2].AsStrMap()
	if err != nil {
		return result, fmt.Errorf("read credit grant: %w", err)
	}
	result.found = len(fields) != 0
	if result.found {
		result.result, err = decodeCreditGrant(fields, c.Grant.ID)
	}
	return result, err
}

func creditGrantCommands(
	client rueidis.DedicatedClient, c *db.CreditGrantCommand, state *creditGrantTransition,
) ([]rueidis.Completed, error) {
	commands := creditPeriodCommands(client, c, state)
	if c.Action == "initialize" {
		return commands, nil
	}
	encoded, err := json.Marshal(&state.result.Grant)
	if err != nil {
		return nil, fmt.Errorf("encode credit grant: %w", err)
	}
	retention := creditReceiptLifetime
	if state.result.Closed && !c.KeepDue {
		retention = creditSettledGrantLifetime
	}
	commands = append(commands, client.B().Hset().Key(c.GrantKey).FieldValue().
		FieldValue("grant", string(encoded)).
		FieldValue("released", strconv.FormatInt(state.result.Released, 10)).
		FieldValue("closed", strconv.FormatBool(state.result.Closed)).
		FieldValue("active", strconv.FormatBool(state.result.Active)).Build(),
		client.B().Pexpire().Key(c.GrantKey).Milliseconds(retention.Milliseconds()).Build())
	if !state.result.Closed {
		active := client.B().Zrem().Key(c.ActiveKey).Member(c.Grant.ID).Build()
		if state.result.Active {
			active = client.B().Zadd().Key(c.ActiveKey).ScoreMember().ScoreMember(0, c.Grant.ID).Build()
		}
		return append(commands, active,
			client.B().Zadd().Key(c.DueKey).ScoreMember().
				ScoreMember(float64(state.result.Grant.ExpiresAt), c.Grant.ID).Build()), nil
	}
	commands = append(commands, client.B().Zrem().Key(c.ActiveKey).Member(c.Grant.ID).Build())
	if c.KeepDue {
		commands = append(commands, client.B().Zadd().Key(c.DueKey).ScoreMember().
			ScoreMember(float64(c.NextCheck), c.Grant.ID).Build())
	} else {
		commands = append(commands, client.B().Zrem().Key(c.DueKey).Member(c.Grant.ID).Build())
	}
	return commands, nil
}

func creditPeriodCommands(
	client rueidis.DedicatedClient, c *db.CreditGrantCommand, state *creditGrantTransition,
) []rueidis.Completed {
	commands := make([]rueidis.Completed, 0, 8)
	for i, period := range []creditAllocationPeriod{state.day, state.month} {
		key := []string{c.DayKey, c.MonthKey}[i]
		commands = append(commands, client.B().Hset().Key(key).FieldValue().
			FieldValue("booked", strconv.FormatInt(period.Booked, 10)).
			FieldValue("limit", strconv.FormatInt(period.Limit, 10)).
			FieldValue("baseline", strconv.FormatInt(period.Baseline, 10)).
			FieldValue("unknown_baseline", strconv.FormatInt(period.UnknownBaseline, 10)).Build(),
			client.B().Pexpire().Key(key).Milliseconds(creditCounterLifetime.Milliseconds()).Build())
	}
	return commands
}

var _ db.CreditStore = (*Store)(nil)
