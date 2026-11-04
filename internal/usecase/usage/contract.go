package usage

import (
	"context"

	domusage "github.com/kailas-cloud/vecdex/internal/domain/usage"
)

// BudgetReader provides current shared usage independently of configured limits.
type BudgetReader interface {
	DailyLimit() int64
	MonthlyLimit() int64
	Snapshot(ctx context.Context) (domusage.Snapshot, error)
}
