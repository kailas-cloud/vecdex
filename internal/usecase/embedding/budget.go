package embedding

import (
	"context"
	"fmt"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/kailas-cloud/vecdex/internal/domain"
)

// BudgetAction defines behavior when token budget is exceeded.
type BudgetAction string

const (
	// BudgetActionWarn logs a warning but allows the request.
	BudgetActionWarn BudgetAction = "warn"
	// BudgetActionReject blocks the request.
	BudgetActionReject BudgetAction = "reject"
)

// BudgetStore is the persistence interface for budget counters.
// Implementations must be idempotent (IncrBy can be called repeatedly).
type BudgetStore interface {
	IncrBy(ctx context.Context, key string, val int64) error
	Get(ctx context.Context, key string) (int64, error)
}

// BudgetTracker is an in-memory token budget tracker with optional persistence.
// Hot path (Check) is in-memory only, no round-trip.
// Record updates in-memory first, then write-behind to store.
type BudgetTracker struct {
	mu             sync.Mutex
	dailyUsed      int64
	monthlyUsed    int64
	dailyLimit     int64
	monthlyLimit   int64
	action         BudgetAction
	provider       string
	lastDayReset   time.Time
	lastMonthReset time.Time
	store          BudgetStore
	logger         *zap.Logger
}

// NewBudgetTracker creates a budget tracker with the given limits.
func NewBudgetTracker(
	provider string, dailyLimit, monthlyLimit int64,
	action BudgetAction, logger *zap.Logger,
) *BudgetTracker {
	now := time.Now().UTC()
	return &BudgetTracker{
		dailyLimit:     dailyLimit,
		monthlyLimit:   monthlyLimit,
		action:         action,
		provider:       provider,
		lastDayReset:   truncateToDay(now),
		lastMonthReset: truncateToMonth(now),
		logger:         logger,
	}
}

// WithStore attaches a persistence store and loads current counters.
func (b *BudgetTracker) WithStore(ctx context.Context, store BudgetStore) *BudgetTracker {
	b.store = store
	b.loadFromStore(ctx)
	return b
}

func (b *BudgetTracker) loadFromStore(ctx context.Context) {
	b.mu.Lock()
	defer b.mu.Unlock()

	now := time.Now().UTC()
	dailyKey := b.dailyKey(now)
	monthlyKey := b.monthlyKey(now)

	if val, err := b.store.Get(ctx, dailyKey); err == nil {
		b.dailyUsed = val
	} else {
		b.logger.Warn("Failed to load daily budget from store", zap.Error(err))
	}

	if val, err := b.store.Get(ctx, monthlyKey); err == nil {
		b.monthlyUsed = val
	} else {
		b.logger.Warn("Failed to load monthly budget from store", zap.Error(err))
	}

	b.logger.Info("Budget loaded from store",
		zap.String("provider", b.provider),
		zap.Int64("daily_used", b.dailyUsed),
		zap.Int64("monthly_used", b.monthlyUsed),
	)
}

func (b *BudgetTracker) dailyKey(t time.Time) string {
	return fmt.Sprintf("%sbudget:%s:daily:%s", domain.KeyPrefix, b.provider, t.Format("2006-01-02"))
}

func (b *BudgetTracker) monthlyKey(t time.Time) string {
	return fmt.Sprintf("%sbudget:%s:monthly:%s", domain.KeyPrefix, b.provider, t.Format("2006-01"))
}

// Check verifies the budget allows a new request. In-memory only (hot path).
func (b *BudgetTracker) Check(_ context.Context) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.resetIfNeeded()

	dailyExceeded := b.dailyLimit > 0 && b.dailyUsed >= b.dailyLimit
	monthlyExceeded := b.monthlyLimit > 0 && b.monthlyUsed >= b.monthlyLimit

	if !dailyExceeded && !monthlyExceeded {
		return nil
	}

	if b.action == BudgetActionReject {
		return domain.ErrEmbeddingQuotaExceeded
	}

	// action=warn: log but allow the request through
	b.logger.Warn("Token budget exceeded",
		zap.String("provider", b.provider),
		zap.Int64("daily_used", b.dailyUsed),
		zap.Int64("daily_limit", b.dailyLimit),
		zap.Int64("monthly_used", b.monthlyUsed),
		zap.Int64("monthly_limit", b.monthlyLimit),
	)
	return nil
}

// Record registers consumed tokens after a request.
// Updates in-memory counters, then write-behind to store (if attached).
func (b *BudgetTracker) Record(tokens int64) {
	b.mu.Lock()
	b.resetIfNeeded()
	b.dailyUsed += tokens
	b.monthlyUsed += tokens
	store := b.store
	now := time.Now().UTC()
	dailyKey := b.dailyKey(now)
	monthlyKey := b.monthlyKey(now)
	b.mu.Unlock()

	if store == nil {
		return
	}

	// Write-behind: fire-and-forget INCRBY to store.
	// Uses background context so store writes don't block the caller.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if err := store.IncrBy(ctx, dailyKey, tokens); err != nil {
		b.logger.Warn("Failed to persist daily budget", zap.String("key", dailyKey), zap.Error(err))
	}
	if err := store.IncrBy(ctx, monthlyKey, tokens); err != nil {
		b.logger.Warn("Failed to persist monthly budget", zap.String("key", monthlyKey), zap.Error(err))
	}
}

// RemainingDaily returns tokens left in the daily budget (-1 if unlimited).
func (b *BudgetTracker) RemainingDaily() int64 {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.resetIfNeeded()
	if b.dailyLimit == 0 {
		return -1 // unlimited
	}
	remaining := b.dailyLimit - b.dailyUsed
	if remaining < 0 {
		return 0
	}
	return remaining
}

// RemainingMonthly returns tokens left in the monthly budget (-1 if unlimited).
func (b *BudgetTracker) RemainingMonthly() int64 {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.resetIfNeeded()
	if b.monthlyLimit == 0 {
		return -1 // unlimited
	}
	remaining := b.monthlyLimit - b.monthlyUsed
	if remaining < 0 {
		return 0
	}
	return remaining
}

// DailyLimit returns the daily token cap.
func (b *BudgetTracker) DailyLimit() int64 { return b.dailyLimit }

// MonthlyLimit returns the monthly token cap.
func (b *BudgetTracker) MonthlyLimit() int64 { return b.monthlyLimit }

// DailyUsed returns tokens consumed today.
func (b *BudgetTracker) DailyUsed() int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.resetIfNeeded()
	return b.dailyUsed
}

// MonthlyUsed returns tokens consumed this month.
func (b *BudgetTracker) MonthlyUsed() int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.resetIfNeeded()
	return b.monthlyUsed
}

// resetIfNeeded zeroes counters when the day or month rolls over.
func (b *BudgetTracker) resetIfNeeded() {
	now := time.Now().UTC()
	today := truncateToDay(now)
	thisMonth := truncateToMonth(now)

	if today.After(b.lastDayReset) {
		b.dailyUsed = 0
		b.lastDayReset = today
	}
	if thisMonth.After(b.lastMonthReset) {
		b.monthlyUsed = 0
		b.lastMonthReset = thisMonth
	}
}

func truncateToDay(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

func truncateToMonth(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
}
