package budget

import (
	"context"
	"crypto/sha256"
	"fmt"
	"math"
	"time"

	"github.com/kailas-cloud/vecdex/internal/db"
	domusage "github.com/kailas-cloud/vecdex/internal/domain/usage"
)

// UsagePartitionCount is stable across physical shard count and replica count.
const UsagePartitionCount = 256

func usagePartition(id string) int {
	digest := sha256.Sum256([]byte(id))
	return int(digest[0])
}

func usageKeys(provider, id string, partition int, now time.Time) []string {
	base := fmt.Sprintf("vecdex:v3:usage:{%x:%d}", sha256.Sum256([]byte(provider)), partition)
	return []string{base + ":day:" + now.UTC().Format("2006-01-02"),
		base + ":month:" + now.UTC().Format("2006-01"), base + ":total", base + ":request:" + id}
}

func usageCommand(provider, id string, now time.Time, action string, tokens int64, unknown bool) *db.BudgetCommand {
	return &db.BudgetCommand{Keys: usageKeys(provider, id, usagePartition(id), now),
		Action: action, Tokens: tokens, Unknown: unknown}
}

// ReserveUsage registers a non-rejecting invocation on its own stable partition.
// This path never updates the shared strict-budget counters.
func (s *Accounting) ReserveUsage(ctx context.Context, provider, id string, now time.Time) error {
	_, err := s.store.Budget(ctx, usageCommand(provider, id, now, "reserve", 0, false))
	if err != nil {
		return fmt.Errorf("begin partitioned usage: %w", err)
	}
	return nil
}

// CompleteUsage reconciles a source invocation idempotently on the same partition.
func (s *Accounting) CompleteUsage(
	ctx context.Context, provider, id string, now time.Time, tokens int64, unknown bool,
) error {
	_, err := s.store.Budget(ctx, usageCommand(provider, id, now, "complete", tokens, unknown))
	if err != nil {
		return fmt.Errorf("complete partitioned usage: %w", err)
	}
	return nil
}

func sumSnapshots(snapshots []domusage.Snapshot) (domusage.Snapshot, error) {
	var result domusage.Snapshot
	for _, snapshot := range snapshots {
		for _, pair := range []struct{ target, source *domusage.Counters }{
			{&result.Day, &snapshot.Day}, {&result.Month, &snapshot.Month}, {&result.Total, &snapshot.Total},
		} {
			if err := addCounters(pair.target, pair.source); err != nil {
				return domusage.Snapshot{}, err
			}
		}
	}
	return result, nil
}

func addCounters(target, source *domusage.Counters) error {
	for _, pair := range []struct {
		target *int64
		source int64
	}{
		{&target.Tokens, source.Tokens}, {&target.Reserved, source.Reserved},
		{&target.Requests, source.Requests}, {&target.Unknown, source.Unknown},
	} {
		if pair.source < 0 || pair.source > math.MaxInt64-*pair.target {
			return fmt.Errorf("shared usage counter overflow")
		}
		*pair.target += pair.source
	}
	return nil
}
