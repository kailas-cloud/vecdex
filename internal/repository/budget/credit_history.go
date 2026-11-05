package budget

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"time"

	domusage "github.com/kailas-cloud/vecdex/internal/domain/usage"
)

func (s *Accounting) initializeCreditHistory(
	ctx context.Context, provider string, now time.Time, daily, monthly int64,
) error {
	groups := make([][]string, 0, UsagePartitionCount+1)
	groups = append(groups, accountingKeys(provider, "", now)[:3])
	for partition := range UsagePartitionCount {
		groups = append(groups, usageKeys(provider, "", partition, now)[:3])
	}
	snapshots, err := s.store.BudgetSnapshots(ctx, groups)
	if err != nil {
		return fmt.Errorf("read prior budget history: %w", err)
	}
	if len(snapshots) != len(groups) {
		return fmt.Errorf("prior budget history partition count mismatch")
	}
	history, err := sumSnapshots(snapshots)
	if err != nil {
		return err
	}
	c := creditControl(provider, "bootstrap", now)
	c.Action, c.DailyLimit, c.MonthlyLimit = "initialize", daily, monthly
	c.BaselineDay, c.UnknownDay, err = creditHistoryBaseline(&history.Day, daily)
	if err != nil {
		return err
	}
	c.BaselineMonth, c.UnknownMonth, err = creditHistoryBaseline(&history.Month, monthly)
	if err != nil {
		return err
	}
	if _, err := s.credits.CreditGrant(ctx, c); err != nil {
		return fmt.Errorf("initialize shared credit history: %w", err)
	}
	return nil
}

func creditHistoryBaseline(counter *domusage.Counters, limit int64) (baseline, unknown int64, err error) {
	if counter.Reserved > math.MaxInt64-counter.Tokens {
		return 0, 0, fmt.Errorf("credit history overflow")
	}
	baseline = counter.Tokens + counter.Reserved
	if counter.Unknown > 0 && limit > baseline {
		unknown = limit - baseline
	}
	return baseline, unknown, nil
}

func (s *Accounting) includeCreditHistory(
	ctx context.Context, provider string, now time.Time, result *domusage.Snapshot,
) (domusage.Snapshot, error) {
	if s.credits == nil {
		return *result, nil
	}
	control := creditControl(provider, "", now)
	fields, err := s.credits.HGetAllMulti(ctx, []string{control.DayKey, control.MonthKey})
	if err != nil {
		return domusage.Snapshot{}, fmt.Errorf("read shared credit history: %w", err)
	}
	if len(fields) != 2 {
		return domusage.Snapshot{}, fmt.Errorf("credit history period count mismatch")
	}
	for i, counter := range []*domusage.Counters{&result.Day, &result.Month} {
		penalty, err := creditHistoryPenalty(fields[i])
		if err != nil {
			return domusage.Snapshot{}, err
		}
		if err := addCounters(counter, &domusage.Counters{Reserved: penalty}); err != nil {
			return domusage.Snapshot{}, err
		}
	}
	return *result, nil
}

func creditHistoryPenalty(fields map[string]string) (int64, error) {
	if len(fields) == 0 {
		return 0, nil
	}
	numbers := make([]int64, 4)
	for i, key := range []string{"booked", "limit", "baseline", "unknown_baseline"} {
		value, err := strconv.ParseInt(fields[key], 10, 64)
		if err != nil || value < 0 {
			return 0, fmt.Errorf("invalid shared credit history field %s", key)
		}
		numbers[i] = value
	}
	if numbers[3] > math.MaxInt64-numbers[2] || numbers[0] < numbers[2]+numbers[3] {
		return 0, fmt.Errorf("credit history exceeds allocation")
	}
	return numbers[3], nil
}
