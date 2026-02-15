package embedding

import (
	"context"
	"errors"
	"sync"
	"testing"

	"go.uber.org/zap"

	"github.com/kailas-cloud/vecdex/internal/domain"
)

func TestBudgetTracker_RejectWhenExceeded(t *testing.T) {
	bt := NewBudgetTracker("test", 100, 0, BudgetActionReject, zap.NewNop())

	bt.Record(100)

	err := bt.Check(context.Background())
	if !errors.Is(err, domain.ErrEmbeddingQuotaExceeded) {
		t.Fatalf("expected domain.ErrEmbeddingQuotaExceeded, got %v", err)
	}
}

func TestBudgetTracker_WarnWhenExceeded(t *testing.T) {
	bt := NewBudgetTracker("test", 100, 0, BudgetActionWarn, zap.NewNop())

	bt.Record(200)

	err := bt.Check(context.Background())
	if err != nil {
		t.Fatalf("expected nil error for warn action, got %v", err)
	}
}

func TestBudgetTracker_MonthlyReject(t *testing.T) {
	bt := NewBudgetTracker("test", 0, 500, BudgetActionReject, zap.NewNop())

	bt.Record(500)

	err := bt.Check(context.Background())
	if !errors.Is(err, domain.ErrEmbeddingQuotaExceeded) {
		t.Fatalf("expected domain.ErrEmbeddingQuotaExceeded for monthly limit, got %v", err)
	}
}

func TestBudgetTracker_UnlimitedWhenZero(t *testing.T) {
	bt := NewBudgetTracker("test", 0, 0, BudgetActionReject, zap.NewNop())

	bt.Record(999999999)

	err := bt.Check(context.Background())
	if err != nil {
		t.Fatalf("expected nil error for unlimited budget, got %v", err)
	}
}

func TestBudgetTracker_Remaining(t *testing.T) {
	bt := NewBudgetTracker("test", 1000, 10000, BudgetActionWarn, zap.NewNop())

	bt.Record(300)

	daily := bt.RemainingDaily()
	if daily != 700 {
		t.Errorf("expected daily remaining 700, got %d", daily)
	}

	monthly := bt.RemainingMonthly()
	if monthly != 9700 {
		t.Errorf("expected monthly remaining 9700, got %d", monthly)
	}
}

func TestBudgetTracker_RemainingUnlimited(t *testing.T) {
	bt := NewBudgetTracker("test", 0, 0, BudgetActionWarn, zap.NewNop())

	daily := bt.RemainingDaily()
	if daily != -1 {
		t.Errorf("expected -1 for unlimited daily, got %d", daily)
	}

	monthly := bt.RemainingMonthly()
	if monthly != -1 {
		t.Errorf("expected -1 for unlimited monthly, got %d", monthly)
	}
}

func TestBudgetTracker_BelowLimitAllows(t *testing.T) {
	bt := NewBudgetTracker("test", 1000, 10000, BudgetActionReject, zap.NewNop())

	bt.Record(500)

	err := bt.Check(context.Background())
	if err != nil {
		t.Fatalf("expected nil error when below limit, got %v", err)
	}
}

// --- Mock BudgetStore ---

type mockBudgetStore struct {
	mu     sync.Mutex
	data   map[string]int64
	getErr error
	setErr error
}

func newMockBudgetStore() *mockBudgetStore {
	return &mockBudgetStore{data: make(map[string]int64)}
}

func (m *mockBudgetStore) IncrBy(_ context.Context, key string, val int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.setErr != nil {
		return m.setErr
	}
	m.data[key] += val
	return nil
}

func (m *mockBudgetStore) Get(_ context.Context, key string) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.getErr != nil {
		return 0, m.getErr
	}
	return m.data[key], nil
}

// --- Persistence tests ---

func TestBudgetTracker_WithStore_LoadsValues(t *testing.T) {
	store := newMockBudgetStore()

	// Pre-seed store with budget values
	bt := NewBudgetTracker("prov", 1000, 10000, BudgetActionReject, zap.NewNop())
	dailyKey := bt.dailyKey(truncateToDay(bt.lastDayReset))
	monthlyKey := bt.monthlyKey(truncateToMonth(bt.lastMonthReset))
	store.data[dailyKey] = 300
	store.data[monthlyKey] = 5000

	bt.WithStore(context.Background(), store)

	if bt.DailyUsed() != 300 {
		t.Errorf("expected daily_used=300, got %d", bt.DailyUsed())
	}
	if bt.MonthlyUsed() != 5000 {
		t.Errorf("expected monthly_used=5000, got %d", bt.MonthlyUsed())
	}
}

func TestBudgetTracker_Record_PersistsToStore(t *testing.T) {
	store := newMockBudgetStore()
	bt := NewBudgetTracker("prov", 1000, 10000, BudgetActionWarn, zap.NewNop())
	bt.WithStore(context.Background(), store)

	bt.Record(42)

	// In-memory updated
	if bt.DailyUsed() != 42 {
		t.Errorf("expected daily_used=42, got %d", bt.DailyUsed())
	}

	// Store updated via write-behind
	store.mu.Lock()
	var dailyStored int64
	for k, v := range store.data {
		if len(k) > 7 {
			dailyStored = v
			break
		}
	}
	store.mu.Unlock()

	if dailyStored != 42 {
		t.Errorf("expected store daily=42, got %d", dailyStored)
	}
}

func TestBudgetTracker_Record_MultipleIncrements(t *testing.T) {
	store := newMockBudgetStore()
	bt := NewBudgetTracker("prov", 10000, 100000, BudgetActionWarn, zap.NewNop())
	bt.WithStore(context.Background(), store)

	bt.Record(100)
	bt.Record(200)
	bt.Record(300)

	if bt.DailyUsed() != 600 {
		t.Errorf("expected daily_used=600, got %d", bt.DailyUsed())
	}

	// Store should contain accumulated values
	dailyKey := bt.dailyKey(truncateToDay(bt.lastDayReset))
	store.mu.Lock()
	val := store.data[dailyKey]
	store.mu.Unlock()
	if val != 600 {
		t.Errorf("expected store daily=600, got %d", val)
	}
}

func TestBudgetTracker_WithStore_LoadError(t *testing.T) {
	store := newMockBudgetStore()
	store.getErr = errors.New("connection refused")

	bt := NewBudgetTracker("prov", 1000, 10000, BudgetActionReject, zap.NewNop())
	bt.WithStore(context.Background(), store)

	// Should fall back to 0 on load error
	if bt.DailyUsed() != 0 {
		t.Errorf("expected daily_used=0 on load error, got %d", bt.DailyUsed())
	}
	if bt.MonthlyUsed() != 0 {
		t.Errorf("expected monthly_used=0 on load error, got %d", bt.MonthlyUsed())
	}
}

func TestBudgetTracker_Record_StoreWriteError(t *testing.T) {
	store := newMockBudgetStore()
	bt := NewBudgetTracker("prov", 1000, 10000, BudgetActionWarn, zap.NewNop())
	bt.WithStore(context.Background(), store)

	// Break store after initial load
	store.mu.Lock()
	store.setErr = errors.New("write timeout")
	store.mu.Unlock()

	// Record must not panic -- in-memory updates, store error is logged
	bt.Record(50)

	if bt.DailyUsed() != 50 {
		t.Errorf("expected daily_used=50 even with store error, got %d", bt.DailyUsed())
	}
}

func TestBudgetTracker_WithStore_CheckStillInMemory(t *testing.T) {
	store := newMockBudgetStore()
	bt := NewBudgetTracker("prov", 100, 0, BudgetActionReject, zap.NewNop())
	bt.WithStore(context.Background(), store)

	bt.Record(100)

	// Check is hot path, in-memory only
	err := bt.Check(context.Background())
	if !errors.Is(err, domain.ErrEmbeddingQuotaExceeded) {
		t.Fatalf("expected domain.ErrEmbeddingQuotaExceeded, got %v", err)
	}
}

func TestBudgetTracker_NoStore_RecordWorks(t *testing.T) {
	// Without store, Record works in-memory only without panicking
	bt := NewBudgetTracker("prov", 1000, 10000, BudgetActionWarn, zap.NewNop())

	bt.Record(42)

	if bt.DailyUsed() != 42 {
		t.Errorf("expected daily_used=42, got %d", bt.DailyUsed())
	}
}

func TestBudgetTracker_DailyKey_Format(t *testing.T) {
	bt := NewBudgetTracker("nebius", 0, 0, BudgetActionWarn, zap.NewNop())
	key := bt.dailyKey(truncateToDay(bt.lastDayReset))

	if key == "" {
		t.Fatal("expected non-empty daily key")
	}
	// Format: vecdex:budget:nebius:daily:YYYY-MM-DD
	if len(key) < 30 {
		t.Errorf("daily key too short: %s", key)
	}
}

func TestBudgetTracker_MonthlyKey_Format(t *testing.T) {
	bt := NewBudgetTracker("nebius", 0, 0, BudgetActionWarn, zap.NewNop())
	key := bt.monthlyKey(truncateToMonth(bt.lastMonthReset))

	if key == "" {
		t.Fatal("expected non-empty monthly key")
	}
	// Format: vecdex:budget:nebius:monthly:YYYY-MM
	if len(key) < 28 {
		t.Errorf("monthly key too short: %s", key)
	}
}
