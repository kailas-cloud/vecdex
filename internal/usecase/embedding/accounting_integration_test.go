package embedding

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kailas-cloud/vecdex/internal/db/valkey"
	"github.com/kailas-cloud/vecdex/internal/domain"
	budgetrepo "github.com/kailas-cloud/vecdex/internal/repository/budget"
	"go.uber.org/zap"
)

func TestSharedBudgetLive(t *testing.T) {
	addr := os.Getenv("VECDEX_TEST_VALKEY_ADDR")
	if addr == "" {
		t.Skip("VECDEX_TEST_VALKEY_ADDR is not set")
	}
	store, err := valkey.NewStore(valkey.Config{Addrs: []string{addr}, Standalone: true})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	repository := budgetrepo.NewAccounting(store)
	provider := "shared-test-" + rand.Text()
	defer cleanupBudgetTest(t, store, provider)

	replicas := []*BudgetTracker{
		NewBudgetTracker(provider, 20, 100, BudgetActionReject, zap.NewNop()).WithAccountingStore(repository, 10),
		NewBudgetTracker(provider, 20, 100, BudgetActionReject, zap.NewNop()).WithAccountingStore(repository, 10),
	}
	completions := raceBudgetReservations(t, replicas)
	first, second := <-completions, <-completions
	first(3, false)
	first(3, false)
	second(0, true)
	snapshot, err := replicas[1].Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Day.Tokens != 3 || snapshot.Day.Reserved != 10 || snapshot.Day.Requests != 2 ||
		snapshot.Day.Unknown != 1 {
		t.Fatalf("incorrect shared reconciliation: %+v", snapshot)
	}
	if _, err := replicas[0].Begin(context.Background(), 1); !errors.Is(err, domain.ErrEmbeddingQuotaExceeded) {
		t.Fatalf("unknown reservation was released: %v", err)
	}
	// A fresh replica sees both known usage and the retained unknown reservation.
	fresh := NewBudgetTracker(provider, 20, 100, BudgetActionReject, zap.NewNop()).WithAccountingStore(repository, 10)
	reread, err := fresh.Snapshot(context.Background())
	if err != nil || reread != snapshot {
		t.Fatalf("restart: %+v %v", reread, err)
	}
}

func budgetTestOwnKey(provider, key string) bool {
	return strings.HasPrefix(key, fmt.Sprintf("vecdex:v2:budget:{%x}:", sha256.Sum256([]byte(provider))))
}

func cleanupBudgetTest(t *testing.T, store *valkey.Store, provider string) {
	t.Helper()
	keys, err := store.Scan(context.Background(), "vecdex:v2:budget:*")
	if err != nil {
		t.Error(err)
		return
	}
	for _, key := range keys {
		if budgetTestOwnKey(provider, key) {
			if err := store.Del(context.Background(), key); err != nil {
				t.Error(err)
			}
		}
	}
}

func raceBudgetReservations(t *testing.T, replicas []*BudgetTracker) chan func(int, bool) {
	t.Helper()
	completions := make(chan func(int, bool), 32)
	failures := make(chan error, 32)
	var workers sync.WaitGroup
	for i := range 32 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			complete, beginErr := replicas[i%2].Begin(context.Background(), 1)
			if beginErr != nil {
				failures <- beginErr
				return
			}
			completions <- complete
		}()
	}
	workers.Wait()
	close(completions)
	close(failures)
	if len(completions) != 2 {
		t.Fatalf("admitted %d requests, expected two", len(completions))
	}
	for failure := range failures {
		if !errors.Is(failure, domain.ErrEmbeddingQuotaExceeded) {
			t.Fatal(failure)
		}
	}
	return completions
}

func TestSharedBudgetRolloverLive(t *testing.T) {
	addr := os.Getenv("VECDEX_TEST_VALKEY_ADDR")
	if addr == "" {
		t.Skip("VECDEX_TEST_VALKEY_ADDR is not set")
	}
	store, err := valkey.NewStore(valkey.Config{Addrs: []string{addr}, Standalone: true})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	repository := budgetrepo.NewAccounting(store)
	provider := "rollover-test-" + rand.Text()
	defer cleanupBudgetTest(t, store, provider)
	ctx := context.Background()
	now := time.Now().UTC()
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	previous := monthStart.Add(-time.Second)
	if _, err := repository.Reserve(ctx, provider, "old-request", previous, 10, 100, 10); err != nil {
		t.Fatal(err)
	}
	// The monthly check must reject before changing the otherwise available daily budget.
	if _, err := repository.Reserve(
		ctx,
		provider,
		"rejected",
		previous,
		1,
		100,
		10,
	); !errors.Is(
		err,
		domain.ErrEmbeddingQuotaExceeded,
	) {
		t.Fatalf("monthly limit ignored: %v", err)
	}
	snapshot, err := repository.Complete(ctx, provider, "old-request", previous, 4, false)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Day.Reserved != 0 || snapshot.Month.Tokens != 4 || snapshot.Total.Tokens != 4 {
		t.Fatalf("settlement: %+v", snapshot)
	}
	if _, err := repository.Complete(ctx, provider, "old-request", previous, 4, false); err != nil {
		t.Fatal(err)
	}
	current, err := repository.Snapshot(ctx, provider, now)
	if err != nil {
		t.Fatal(err)
	}
	if current.Day.Tokens != 0 || current.Month.Tokens != 0 || current.Total.Tokens != 4 ||
		current.Total.Requests != 1 {
		t.Fatalf("lifetime counter or original period lost: %+v", current)
	}
}

func TestSharedBudgetUnavailableLive(t *testing.T) {
	addr := os.Getenv("VECDEX_TEST_VALKEY_ADDR")
	if addr == "" {
		t.Skip("VECDEX_TEST_VALKEY_ADDR is not set")
	}
	store, err := valkey.NewStore(valkey.Config{Addrs: []string{addr}, Standalone: true})
	if err != nil {
		t.Fatal(err)
	}
	store.Close()
	tracker := NewBudgetTracker("unavailable", 100, 100, BudgetActionReject, zap.NewNop()).
		WithAccountingStore(budgetrepo.NewAccounting(store), 10)
	complete, err := tracker.Begin(context.Background(), 1)
	if err == nil || complete != nil {
		t.Fatalf("invocation admitted without shared store: %v", err)
	}
}
