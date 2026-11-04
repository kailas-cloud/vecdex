package db

import (
	"context"

	domusage "github.com/kailas-cloud/vecdex/internal/domain/usage"
)

// BudgetCommand changes or reads shared accounting in one collection-independent hash slot.
type BudgetCommand struct {
	Keys                     []string
	Action                   string
	Amount, Tokens           int64
	DailyLimit, MonthlyLimit int64
	Unknown                  bool
}

// BudgetStore provides atomic reservations and usage accounting across replicas.
type BudgetStore interface {
	Budget(ctx context.Context, command *BudgetCommand) (domusage.Snapshot, error)
}
