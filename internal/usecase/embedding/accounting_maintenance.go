package embedding

import (
	"context"
	"errors"
	"sync"
	"time"

	"go.uber.org/zap"
)

// StartMaintenance recovers expired strict credits and refreshes metrics at a
// bounded rate. Normal admission/completion never reads global usage.
// Stop waits for the worker before the database connection is closed.
func (b *BudgetTracker) StartMaintenance(parent context.Context) func() {
	if b.accounting == nil || (b.dailyLimit == 0 && b.monthlyLimit == 0) {
		return func() {}
	}
	ctx, cancel := context.WithCancel(parent)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				read, finish := context.WithTimeout(ctx, 2*time.Second)
				err := b.refreshAccounting(read)
				finish()
				if err != nil && ctx.Err() == nil {
					b.logger.Warn("Embedding usage refresh failed", zap.Error(err))
				}
			}
		}
	}()
	var once sync.Once
	return func() { once.Do(func() { cancel(); <-done }) }
}

func (b *BudgetTracker) refreshAccounting(ctx context.Context) error {
	var recoveryErr error
	if credits, ok := b.accounting.(CreditAccountingStore); ok && b.strictBudget() {
		recoveryErr = credits.ReconcileCredits(ctx, b.provider, time.Now().UTC(), 32)
	}
	_, snapshotErr := b.Snapshot(ctx)
	return errors.Join(recoveryErr, snapshotErr)
}
