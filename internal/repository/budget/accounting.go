package budget

import (
	"context"
	"crypto/sha256"
	"fmt"
	"time"

	"github.com/kailas-cloud/vecdex/internal/db"
	domusage "github.com/kailas-cloud/vecdex/internal/domain/usage"
)

// Accounting owns versioned shared counters; all keys use a provider hash slot.
type Accounting struct{ store db.BudgetStore }

// NewAccounting creates the shared accounting repository.
func NewAccounting(store db.BudgetStore) *Accounting { return &Accounting{store: store} }

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

// Snapshot reads all three periods consistently, including reservations from other replicas.
func (s *Accounting) Snapshot(ctx context.Context, provider string, now time.Time) (domusage.Snapshot, error) {
	result, err := s.store.Budget(
		ctx,
		&db.BudgetCommand{Keys: accountingKeys(provider, "snapshot", now), Action: "read"},
	)
	if err != nil {
		return result, fmt.Errorf("read embedding usage: %w", err)
	}
	return result, nil
}
