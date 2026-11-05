package valkey

import (
	"context"
	"errors"
	"fmt"
	"sync"

	domusage "github.com/kailas-cloud/vecdex/internal/domain/usage"
)

const budgetSnapshotParallelism = 16

// BudgetSnapshots bounds fan-out and fails the whole read if any owner fails.
func (s *Store) BudgetSnapshots(ctx context.Context, groups [][]string) ([]domusage.Snapshot, error) {
	for _, group := range groups {
		if len(group) != 3 {
			return nil, fmt.Errorf("budget snapshot requires three periods per partition")
		}
	}
	results := make([]domusage.Snapshot, len(groups))
	failures := make([]error, len(groups))
	jobs := make(chan int, len(groups))
	for i := range groups {
		jobs <- i
	}
	close(jobs)
	var workers sync.WaitGroup
	for range min(len(groups), budgetSnapshotParallelism) {
		workers.Go(func() {
			for i := range jobs {
				results[i], failures[i] = s.budgetSnapshotGroup(ctx, groups[i])
			}
		})
	}
	workers.Wait()
	if err := errors.Join(failures...); err != nil {
		return nil, err
	}
	return results, nil
}

func (s *Store) budgetSnapshotGroup(ctx context.Context, keys []string) (domusage.Snapshot, error) {
	for attempt := 0; attempt < maxTransactionAttempts; attempt++ {
		result, committed, err := s.readBudgetSnapshotGroup(ctx, keys)
		if !canRetryTransaction(err) && (err != nil || committed) {
			return result, err
		}
		if err := transactionRetry(ctx, attempt); err != nil {
			return result, err
		}
	}
	return domusage.Snapshot{}, fmt.Errorf("budget snapshot topology changed repeatedly")
}

func (s *Store) readBudgetSnapshotGroup(ctx context.Context, keys []string) (domusage.Snapshot, bool, error) {
	client, release := s.client.Dedicate()
	defer release()
	defer unwatch(client)
	// A keyed read pins the connection to this partition before MULTI. No WATCH
	// is needed for an atomic read-only EXEC, so active writers cannot starve it.
	if err := client.Do(ctx, client.B().Hgetall().Key(keys[0]).Build()).Error(); err != nil {
		if redirectedTransaction(err) {
			return domusage.Snapshot{}, false, &transactionRejectedError{err: err}
		}
		return domusage.Snapshot{}, false, fmt.Errorf("route budget snapshot: %w", err)
	}
	return readBudgetTransaction(ctx, client, keys)
}
