package embedding

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	domusage "github.com/kailas-cloud/vecdex/internal/domain/usage"
	"go.uber.org/zap"
)

type creditObserverRecorder struct {
	partitionedAccountingRecorder
	creditBegins, creditCompletions, recoveries int
	amount                                      int64
	ticket                                      domusage.CreditTicket
	recoveryLimit                               int
}

func (r *creditObserverRecorder) ReserveCredits(
	_ context.Context, provider, id string, now time.Time, amount, daily, monthly int64,
) (domusage.CreditTicket, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.creditBegins++
	r.amount = amount
	r.ticket = domusage.CreditTicket{Provider: provider, ID: id, GrantID: "grant", At: now.UnixMilli(),
		Amount: amount, DailyLimit: daily, MonthlyLimit: monthly}
	return r.ticket, r.beginErr
}

func (r *creditObserverRecorder) CompleteCredits(
	_ context.Context, ticket *domusage.CreditTicket, tokens int64, unknown bool,
) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.creditCompletions++
	if *ticket != r.ticket {
		return errors.New("completion changed the original credit ticket")
	}
	r.tokens, r.unknown = tokens, unknown
	return nil
}

func (r *creditObserverRecorder) ReconcileCredits(_ context.Context, _ string, _ time.Time, limit int) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.recoveries++
	r.recoveryLimit = limit
	return nil
}

func TestStrictCreditInvocationAvoidsSharedAccountingReads(t *testing.T) {
	store := &creditObserverRecorder{}
	tracker := NewBudgetTracker("provider", 1000, 2000, BudgetActionReject, zap.NewNop()).WithAccountingStore(store, 10)
	complete, err := tracker.Begin(context.Background(), 3)
	if err != nil {
		t.Fatal(err)
	}
	complete(7, true)
	complete(7, true)
	if store.creditBegins != 1 || store.creditCompletions != 1 || store.amount != 30 ||
		store.tokens != 7 || !store.unknown || store.reserves != 0 || store.strictCalls != 0 || store.snapshots != 0 {
		t.Fatalf("strict invocation used shared legacy/usage paths or duplicated completion: %+v", store)
	}
}

func TestStrictCreditInvocationFailureDoesNotAdmitUpstream(t *testing.T) {
	want := errors.New("local partition unavailable")
	store := &creditObserverRecorder{partitionedAccountingRecorder: partitionedAccountingRecorder{beginErr: want}}
	tracker := NewBudgetTracker("provider", 1000, 1000, BudgetActionReject, zap.NewNop()).WithAccountingStore(store, 10)
	complete, err := tracker.Begin(context.Background(), 1)
	if complete != nil || !errors.Is(err, want) || store.creditCompletions != 0 {
		t.Fatalf("unaccounted admission: %v", err)
	}
	tracker.maxInputTokens = math.MaxInt64
	if _, err := tracker.Begin(context.Background(), 2); err == nil || store.creditBegins != 1 {
		t.Fatalf("overflowing reservation reached storage: %v", err)
	}
}

func TestStrictCreditMaintenanceRecoversAndStops(t *testing.T) {
	store := &creditObserverRecorder{}
	tracker := NewBudgetTracker("provider", 1000, 1000, BudgetActionReject, zap.NewNop()).WithAccountingStore(store, 10)
	stop := tracker.StartMaintenance(context.Background())
	defer stop()
	deadline := time.Now().Add(3 * time.Second)
	for {
		store.mu.Lock()
		done := store.recoveries > 0 && store.snapshots > 0
		store.mu.Unlock()
		if done {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("strict maintenance never recovered durable grants")
		}
		time.Sleep(10 * time.Millisecond)
	}
	stop()
	stop()
	if store.recoveryLimit != 32 || store.recoveries > 3 {
		t.Fatalf("unbounded credit recovery: %+v", store)
	}
}
