package embedding

import (
	"context"
	"crypto/rand"
	"fmt"
	"math"
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

// UsageAccountingStore records ordinary usage without a shared write-side counter.
type UsageAccountingStore interface {
	ReserveUsage(ctx context.Context, provider, id string, now time.Time) error
	CompleteUsage(ctx context.Context, provider, id string, now time.Time, tokens int64, unknown bool) error
}

// CreditAccountingStore amortizes shared strict-budget control over local pools.
type CreditAccountingStore interface {
	ReserveCredits(context.Context, string, string, time.Time, int64, int64, int64) (domusage.CreditTicket, error)
	CompleteCredits(context.Context, *domusage.CreditTicket, int64, bool) error
	ReconcileCredits(context.Context, string, time.Time, int) error
}

// WithAccountingStore enables shared reservations and independent usage reporting.
// maxInputTokens must be a conservative bound for one model input.
func (b *BudgetTracker) WithAccountingStore(store AccountingStore, maxInputTokens int64) *BudgetTracker {
	b.accounting = store
	b.maxInputTokens = maxInputTokens
	return b
}

func (b *BudgetTracker) sharedAccountingEnabled() bool { return b.accounting != nil }

// Begin reserves the model's upper bound for each input in strict mode. The receipt
// captures the original UTC periods, so completion across midnight charges those periods.
func (b *BudgetTracker) Begin(ctx context.Context, inputs int) (func(int, bool), error) {
	if b.accounting == nil {
		if err := b.Check(ctx); err != nil {
			return nil, err
		}
		return func(tokens int, _ bool) { b.Record(int64(tokens)) }, nil
	}
	if usage, ok := b.accounting.(UsageAccountingStore); ok && !b.strictBudget() {
		return b.beginUsage(ctx, usage)
	}
	if credits, ok := b.accounting.(CreditAccountingStore); ok && b.strictBudget() {
		return b.beginCredits(ctx, credits, inputs)
	}
	now := time.Now().UTC()
	id := rand.Text()
	amount := int64(0)
	if b.action == BudgetActionReject && (b.dailyLimit > 0 || b.monthlyLimit > 0) {
		var err error
		amount, err = b.creditReservationAmount(inputs)
		if err != nil {
			return nil, err
		}
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

func (b *BudgetTracker) creditReservationAmount(inputs int) (int64, error) {
	if inputs <= 0 || b.maxInputTokens <= 0 || b.maxInputTokens > math.MaxInt64/int64(inputs) {
		return 0, fmt.Errorf("invalid embedding reservation bound")
	}
	return b.maxInputTokens * int64(inputs), nil
}

func (b *BudgetTracker) beginCredits(
	ctx context.Context, store CreditAccountingStore, inputs int,
) (func(int, bool), error) {
	amount, err := b.creditReservationAmount(inputs)
	if err != nil {
		return nil, err
	}
	ticket, err := store.ReserveCredits(ctx, b.provider, rand.Text(), time.Now().UTC(),
		amount, b.dailyLimit, b.monthlyLimit)
	if err != nil {
		return nil, fmt.Errorf("begin embedding credit invocation: %w", err)
	}
	var once sync.Once
	return func(tokens int, unknown bool) {
		once.Do(func() {
			complete, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			if err := store.CompleteCredits(complete, &ticket, int64(max(tokens, 0)), unknown); err != nil {
				b.logger.Error("Embedding accounting failed; reservation retained", zap.Error(err))
			}
		})
	}, nil
}

func (b *BudgetTracker) strictBudget() bool {
	return b.action == BudgetActionReject && (b.dailyLimit > 0 || b.monthlyLimit > 0)
}

func (b *BudgetTracker) beginUsage(ctx context.Context, store UsageAccountingStore) (func(int, bool), error) {
	now, id := time.Now().UTC(), rand.Text()
	if err := store.ReserveUsage(ctx, b.provider, id, now); err != nil {
		return nil, fmt.Errorf("begin embedding usage: %w", err)
	}
	if b.dailyLimit == 0 && b.monthlyLimit == 0 {
		b.observeBudget(&domusage.Snapshot{})
	}
	var once sync.Once
	return func(tokens int, unknown bool) {
		once.Do(func() {
			complete, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			if err := store.CompleteUsage(complete, b.provider, id, now, int64(max(tokens, 0)), unknown); err != nil {
				b.logger.Error("Embedding usage accounting failed", zap.Error(err))
			}
		})
	}, nil
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
		b.observeBudget(&result)
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
		if b.action == BudgetActionWarn && remaining == 0 && b.shouldWarn(entry.period, time.Now().UTC()) {
			b.logger.Warn(
				"Token budget exceeded",
				zap.String("provider", b.provider),
				zap.String("period", entry.period),
			)
		}
	}
}

func (b *BudgetTracker) shouldWarn(period string, now time.Time) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	seen, current := &b.warnedDay, truncateToDay(now)
	if period == "monthly" {
		seen, current = &b.warnedMonth, truncateToMonth(now)
	}
	if seen.Equal(current) {
		return false
	}
	*seen = current
	return true
}
