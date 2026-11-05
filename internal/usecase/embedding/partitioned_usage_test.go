package embedding

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	domusage "github.com/kailas-cloud/vecdex/internal/domain/usage"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

type partitionedAccountingRecorder struct {
	mu                             sync.Mutex
	reserves, completes, snapshots int
	strictCalls                    int
	unknown                        bool
	tokens                         int64
	beginErr                       error
}

func (r *partitionedAccountingRecorder) ReserveUsage(context.Context, string, string, time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reserves++
	return r.beginErr
}

func (r *partitionedAccountingRecorder) CompleteUsage(
	_ context.Context, _, _ string, _ time.Time, tokens int64, unknown bool,
) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.completes++
	r.tokens += tokens
	r.unknown = unknown
	return nil
}

func (r *partitionedAccountingRecorder) Reserve(
	context.Context, string, string, time.Time, int64, int64, int64,
) (domusage.Snapshot, error) {
	r.strictCalls++
	return domusage.Snapshot{}, nil
}

func (r *partitionedAccountingRecorder) Complete(
	context.Context, string, string, time.Time, int64, bool,
) (domusage.Snapshot, error) {
	r.strictCalls++
	return domusage.Snapshot{}, nil
}

func (r *partitionedAccountingRecorder) Snapshot(context.Context, string, time.Time) (domusage.Snapshot, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.snapshots++
	return domusage.Snapshot{Day: domusage.Counters{Tokens: r.tokens}}, nil
}

func TestNonRejectingInvocationsAvoidGlobalCountersAndReads(t *testing.T) {
	for _, action := range []BudgetAction{BudgetActionWarn, BudgetActionReject} {
		t.Run(string(action), func(t *testing.T) {
			store := &partitionedAccountingRecorder{}
			limit := int64(1)
			if action == BudgetActionReject {
				limit = 0 // A reject policy with no configured limit is also ordinary usage.
			}
			tracker := NewBudgetTracker("provider", limit, 0, action, zap.NewNop()).WithAccountingStore(store, 100)
			if err := tracker.Check(context.Background()); err != nil {
				t.Fatal(err)
			}
			complete, err := tracker.Begin(context.Background(), 1)
			if err != nil {
				t.Fatal(err)
			}
			complete(10, true)
			complete(10, true)
			if store.reserves != 1 || store.completes != 1 || store.tokens != 10 || !store.unknown ||
				store.strictCalls != 0 || store.snapshots != 0 {
				t.Fatalf("ordinary invocation used a global path or duplicated accounting: %+v", store)
			}
		})
	}
}

func TestOrdinaryUsageStoreFailurePreventsUpstreamAdmission(t *testing.T) {
	want := errors.New("invocation partition unavailable")
	store := &partitionedAccountingRecorder{beginErr: want}
	tracker := NewBudgetTracker("provider", 0, 0, BudgetActionWarn, zap.NewNop()).WithAccountingStore(store, 100)
	complete, err := tracker.Begin(context.Background(), 1)
	if !errors.Is(err, want) || complete != nil || store.completes != 0 {
		t.Fatalf("unaccounted invocation was admitted: %v", err)
	}
}

func TestInstrumentedWrapperDoesNotOverwriteSharedUsageMetrics(t *testing.T) {
	store := &partitionedAccountingRecorder{}
	tracker := NewBudgetTracker("provider", 100, 100, BudgetActionWarn, zap.NewNop()).WithAccountingStore(store, 100)
	wrapper := &InstrumentedEmbedder{budget: tracker}
	wrapper.recordBatchBudget(20)
	if tracker.DailyUsed() != 0 || tracker.MonthlyUsed() != 0 {
		t.Fatal("shared accounting was counted again in local wrapper counters")
	}
}

func TestWarnMaintenanceObservesSharedUsageAndStops(t *testing.T) {
	core, events := observer.New(zap.WarnLevel)
	store := &partitionedAccountingRecorder{tokens: 20}
	tracker := NewBudgetTracker("provider", 10, 0, BudgetActionWarn, zap.New(core)).WithAccountingStore(store, 100)
	stop := tracker.StartMaintenance(context.Background())
	defer stop()
	deadline := time.Now().Add(3 * time.Second)
	for events.FilterMessage("Token budget exceeded").Len() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("warn policy never observed the shared counter")
		}
		time.Sleep(10 * time.Millisecond)
	}
	stop()
	stop()
	store.mu.Lock()
	reads := store.snapshots
	store.mu.Unlock()
	if reads < 1 || reads > 3 {
		t.Fatalf("warn refresh did not use its bounded timer: %d reads", reads)
	}
}

func TestSharedWarnLogsOncePerUTCPeriod(t *testing.T) {
	core, events := observer.New(zap.WarnLevel)
	tracker := NewBudgetTracker("provider", 10, 10, BudgetActionWarn, zap.New(core))
	state := domusage.Snapshot{Day: domusage.Counters{Tokens: 20}, Month: domusage.Counters{Tokens: 20}}
	tracker.observeBudget(&state)
	tracker.observeBudget(&state)
	if events.FilterMessage("Token budget exceeded").Len() != 2 {
		t.Fatal("the same daily/monthly warning repeated without a period change")
	}
	nextMonth := time.Now().UTC().AddDate(0, 1, 0)
	if !tracker.shouldWarn("daily", nextMonth) || !tracker.shouldWarn("monthly", nextMonth) {
		t.Fatal("a previous warning suppressed the next UTC period")
	}
}
