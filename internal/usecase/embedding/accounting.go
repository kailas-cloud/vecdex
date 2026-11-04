package embedding

import (
	"context"
	"crypto/rand"
	"fmt"
	"sync"
	"time"

	domusage "github.com/kailas-cloud/vecdex/internal/domain/usage"
	"github.com/kailas-cloud/vecdex/internal/metrics"
	"go.uber.org/zap"
)

// AccountingStore atomically accounts for invocations across all server replicas.
type AccountingStore interface {
	Reserve(
		ctx context.Context,
		provider, id string,
		now time.Time,
		amount, daily, monthly int64,
	) (domusage.Snapshot, error)
	Complete(
		ctx context.Context,
		provider, id string,
		now time.Time,
		tokens int64,
		unknown bool,
	) (domusage.Snapshot, error)
	Snapshot(ctx context.Context, provider string, now time.Time) (domusage.Snapshot, error)
}

// WithAccountingStore enables shared reservations and independent usage reporting.
// maxInputTokens must be a conservative bound for one model input.
func (b *BudgetTracker) WithAccountingStore(store AccountingStore, maxInputTokens int64) *BudgetTracker {
	b.accounting = store
	b.maxInputTokens = maxInputTokens
	return b
}

// Begin reserves the model's upper bound for each input in strict mode. The receipt
// captures the original UTC periods, so completion across midnight charges those periods.
func (b *BudgetTracker) Begin(ctx context.Context, inputs int) (func(int, bool), error) {
	if b.accounting == nil {
		if err := b.Check(ctx); err != nil {
			return nil, err
		}
		return func(tokens int, _ bool) { b.Record(int64(tokens)) }, nil
	}
	now := time.Now().UTC()
	id := rand.Text()
	amount := int64(0)
	if b.action == BudgetActionReject && (b.dailyLimit > 0 || b.monthlyLimit > 0) {
		amount = b.maxInputTokens * int64(inputs)
	}
	snapshot, err := b.accounting.Reserve(ctx, b.provider, id, now, amount, b.reservationLimit(b.dailyLimit),
		b.reservationLimit(b.monthlyLimit))
	if err != nil {
		return nil, fmt.Errorf("begin embedding invocation: %w", err)
	}
	b.observeBudget(&snapshot)
	var once sync.Once
	return func(tokens int, unknown bool) { once.Do(func() { b.completeInvocation(id, now, tokens, unknown) }) }, nil
}

func (b *BudgetTracker) reservationLimit(limit int64) int64 {
	if b.action == BudgetActionReject {
		return limit
	}
	return 0
}

func (b *BudgetTracker) completeInvocation(id string, now time.Time, tokens int, unknown bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	snapshot, err := b.accounting.Complete(ctx, b.provider, id, now, int64(max(tokens, 0)), unknown)
	if err != nil {
		b.logger.Error("Embedding accounting failed; reservation retained", zap.Error(err))
		return
	}
	b.observeBudget(&snapshot)
}

// Snapshot returns authoritative shared counters; local mode is useful for embedded users.
func (b *BudgetTracker) Snapshot(ctx context.Context) (domusage.Snapshot, error) {
	if b.accounting != nil {
		result, err := b.accounting.Snapshot(ctx, b.provider, time.Now().UTC())
		if err != nil {
			return result, fmt.Errorf("embedding usage: %w", err)
		}
		return result, nil
	}
	return domusage.Snapshot{Day: domusage.Counters{Tokens: b.DailyUsed()},
		Month: domusage.Counters{Tokens: b.MonthlyUsed()}}, nil
}

func (b *BudgetTracker) observeBudget(snapshot *domusage.Snapshot) {
	for _, entry := range []struct {
		period   string
		limit    int64
		counters domusage.Counters
	}{
		{"daily", b.dailyLimit, snapshot.Day}, {"monthly", b.monthlyLimit, snapshot.Month},
	} {
		remaining := int64(-1)
		if entry.limit > 0 {
			remaining = max(int64(0), entry.limit-entry.counters.Tokens-entry.counters.Reserved)
		}
		metrics.EmbeddingBudgetTokensRemaining.WithLabelValues(b.provider, entry.period).Set(float64(remaining))
		if b.action == BudgetActionWarn && remaining == 0 {
			b.logger.Warn(
				"Token budget exceeded",
				zap.String("provider", b.provider),
				zap.String("period", entry.period),
			)
		}
	}
}
