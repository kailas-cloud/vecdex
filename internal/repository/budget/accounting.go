package budget

import (
	"context"
	"crypto/sha256"
	"fmt"
	"sync"
	"time"

	"github.com/kailas-cloud/vecdex/internal/db"
	domusage "github.com/kailas-cloud/vecdex/internal/domain/usage"
)

type accountingStore interface {
	db.BudgetStore
	db.BudgetSnapshotStore
}

// Accounting separates partitioned usage from strict reservation control.
type Accounting struct {
	store   accountingStore
	credits creditBackend
	mu      sync.Mutex
	caches  map[creditPolicy]*creditCache
}

// NewAccounting creates the shared accounting repository.
func NewAccounting(store accountingStore) *Accounting {
	credits, _ := store.(creditBackend)
	return &Accounting{store: store, credits: credits, caches: make(map[creditPolicy]*creditCache)}
}

func accountingKeys(provider, id string, now time.Time) []string {
	base := fmt.Sprintf("vecdex:v2:budget:{%x}", sha256.Sum256([]byte(provider)))
	return []string{base + ":day:" + now.UTC().Format("2006-01-02"),
		base + ":month:" + now.UTC().Format("2006-01"), base + ":total", base + ":request:" + id}
}

// Reserve checks both limits and reserves an invocation before contacting the model.
func (s *Accounting) Reserve(
	ctx context.Context,
	provider, id string,
	now time.Time,
	amount, daily, monthly int64,
) (domusage.Snapshot, error) {
	snapshot, err := s.store.Budget(ctx, &db.BudgetCommand{Keys: accountingKeys(provider, id, now), Action: "reserve",
		Amount: amount, DailyLimit: daily, MonthlyLimit: monthly})
	if err != nil {
		return snapshot, fmt.Errorf("reserve embedding budget: %w", err)
	}
	return snapshot, nil
}

// Complete records measured usage, retaining the reservation when usage is unknown.
func (s *Accounting) Complete(
	ctx context.Context,
	provider, id string,
	now time.Time,
	tokens int64,
	unknown bool,
) (domusage.Snapshot, error) {
	snapshot, err := s.store.Budget(ctx, &db.BudgetCommand{Keys: accountingKeys(provider, id, now), Action: "complete",
		Tokens: tokens, Unknown: unknown})
	if err != nil {
		return snapshot, fmt.Errorf("complete embedding budget: %w", err)
	}
	return snapshot, nil
}

// Snapshot combines measured usage and strict reservations from all replicas.
// Periods within one partition are atomic; partitions can advance independently.
func (s *Accounting) Snapshot(ctx context.Context, provider string, now time.Time) (domusage.Snapshot, error) {
	groups := make([][]string, 0, UsagePartitionCount*2+1)
	// Retain the existing strict/legacy history while usage moves to v3 partitions.
	groups = append(groups, accountingKeys(provider, "snapshot", now)[:3])
	for partition := range UsagePartitionCount {
		groups = append(groups, usageKeys(provider, "snapshot", partition, now)[:3],
			creditUsageKeys(provider, "snapshot", "snapshot", partition, now)[:3])
	}
	snapshots, err := s.store.BudgetSnapshots(ctx, groups)
	if err != nil {
		return domusage.Snapshot{}, fmt.Errorf("read embedding usage: %w", err)
	}
	if len(snapshots) != len(groups) {
		return domusage.Snapshot{}, fmt.Errorf("embedding usage partition count mismatch")
	}
	result, err := sumSnapshots(snapshots)
	if err != nil {
		return result, err
	}
	return s.includeCreditHistory(ctx, provider, now, &result)
}
