package budget

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/kailas-cloud/vecdex/internal/db"
	"github.com/kailas-cloud/vecdex/internal/db/valkey"
	"github.com/kailas-cloud/vecdex/internal/domain"
	domusage "github.com/kailas-cloud/vecdex/internal/domain/usage"
)

type creditRecordingStore struct {
	*valkey.Store
	mu                                                     sync.Mutex
	keys                                                   map[string]bool
	grants, reads, reservations, completions, legacyWrites int
	installs, failInstallAt                                int
	activations, failActivateAt                            int
}

func creditRepositoryFixture(t *testing.T) (*creditRecordingStore, valkey.Config) {
	t.Helper()
	store, cfg := usageFixture(t)
	recording := &creditRecordingStore{Store: store, keys: make(map[string]bool)}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		jobs := make(chan string, len(recording.keys))
		for key := range recording.keys {
			jobs <- key
		}
		close(jobs)
		var workers sync.WaitGroup
		for range 16 {
			workers.Go(func() {
				for key := range jobs {
					if err := store.Del(ctx, key); err != nil {
						t.Error(err)
					}
				}
			})
		}
		workers.Wait()
	})
	return recording, cfg
}

func (s *creditRecordingStore) record(keys ...string) {
	for _, key := range keys {
		if key != "" {
			s.keys[key] = true
		}
	}
}

func (s *creditRecordingStore) CreditGrant(
	ctx context.Context, c *db.CreditGrantCommand,
) (db.CreditGrantResult, error) {
	s.mu.Lock()
	s.record(c.DayKey, c.MonthKey, c.GrantKey, c.ActiveKey, c.DueKey)
	if c.Action == "grant" {
		s.grants++
	}
	if c.Action == "activate" {
		s.activations++
	}
	fail := c.Action == "activate" && s.failActivateAt > 0 && s.activations == s.failActivateAt
	s.mu.Unlock()
	result, err := s.Store.CreditGrant(ctx, c)
	if fail && err == nil {
		err = errors.New("simulated lost activation reply")
	}
	return result, err
}

func (s *creditRecordingStore) CreditPool(ctx context.Context, c *db.CreditPoolCommand) (db.CreditPoolResult, error) {
	s.mu.Lock()
	s.record(c.PoolKey, c.LiveKey, c.ReceiptKey)
	s.record(c.CounterKeys...)
	if c.Action == "reserve" {
		s.reservations++
	}
	if c.Action == "complete" {
		s.completions++
	}
	if c.Action == "install" {
		s.installs++
	}
	fail := c.Action == "install" && s.failInstallAt > 0 && s.installs == s.failInstallAt
	s.mu.Unlock()
	if fail {
		return db.CreditPoolResult{}, errors.New("simulated installer process loss")
	}
	return s.Store.CreditPool(ctx, c)
}

func (s *creditRecordingStore) Budget(ctx context.Context, c *db.BudgetCommand) (domusage.Snapshot, error) {
	s.mu.Lock()
	s.record(c.Keys...)
	s.legacyWrites++
	s.mu.Unlock()
	return s.Store.Budget(ctx, c)
}

func (s *creditRecordingStore) BudgetSnapshots(ctx context.Context, groups [][]string) ([]domusage.Snapshot, error) {
	s.mu.Lock()
	s.reads++
	s.mu.Unlock()
	return s.Store.BudgetSnapshots(ctx, groups)
}

func reserveCredit(t *testing.T, repo *Accounting, provider, id string, amount, limit int64) domusage.CreditTicket {
	t.Helper()
	ticket, err := repo.ReserveCredits(context.Background(), provider, id, time.Now().UTC(), amount, limit, limit)
	if err != nil {
		t.Fatal(err)
	}
	return ticket
}

func completeCredit(t *testing.T, repo *Accounting, ticket *domusage.CreditTicket, tokens int64, unknown bool) {
	t.Helper()
	if err := repo.CompleteCredits(context.Background(), ticket, tokens, unknown); err != nil {
		t.Fatal(err)
	}
}

func TestLeasedCreditAmortizesSharedControlAndSpreadsUsageLive(t *testing.T) {
	store, cfg := creditRepositoryFixture(t)
	repo, provider := NewAccounting(store), "leased-distribution-"+rand.Text()
	const invocations = 256
	for i := range invocations {
		ticket := reserveCredit(t, repo, provider, fmt.Sprintf("invocation-%d", i), 10, 1_000_000)
		completeCredit(t, repo, &ticket, 7, false)
		completeCredit(t, repo, &ticket, 7, false)
	}
	if store.grants >= invocations/8 || store.reads != 1 || store.legacyWrites != 0 {
		t.Fatalf("per-invocation shared control remained: grants=%d reads=%d legacy=%d",
			store.grants, store.reads, store.legacyWrites)
	}
	if store.reservations != invocations || store.completions != invocations*2 {
		t.Fatal("invocation count mismatch")
	}
	view, err := repo.Snapshot(context.Background(), provider, time.Now().UTC())
	want := domusage.Counters{Tokens: invocations * 7, Requests: invocations}
	if err != nil || view.Day != want || view.Month != want || view.Total != want {
		t.Fatalf("idle credits or duplicate completions changed measured usage: %+v %v", view, err)
	}
	assertCreditPartitionDistribution(t, store, cfg, provider, invocations)
}

func assertCreditPartitionDistribution(
	t *testing.T, store *creditRecordingStore, cfg valkey.Config, provider string, invocations int,
) {
	t.Helper()
	groups := make([][]string, UsagePartitionCount)
	for partition := range groups {
		groups[partition] = creditUsageKeys(provider, "", "", partition, time.Now().UTC())[:3]
	}
	partitions, err := store.Store.BudgetSnapshots(context.Background(), groups)
	if err != nil {
		t.Fatal(err)
	}
	populated := 0
	for _, partition := range partitions {
		if partition.Total.Requests > 0 {
			populated++
		}
	}
	if populated < 16 {
		t.Fatalf("only %d virtual partitions used", populated)
	}
	t.Logf("%d invocations: %d shared grant requests, %d initial history read, %d populated partitions",
		invocations, store.grants, store.reads-1, populated)
	if !cfg.Standalone {
		assertUsagePrimaryDistribution(t, context.Background(), cfg, groups, partitions)
	}
}

func TestLeasedCreditReplicasShareLimitsAndRecoverFragmentedCreditLive(t *testing.T) {
	store, _ := creditRepositoryFixture(t)
	provider := "leased-shared-" + rand.Text()
	replicas := []*Accounting{NewAccounting(store), NewAccounting(store)}
	accepted := make(chan domusage.CreditTicket, 32)
	failures := make(chan error, 32)
	var workers sync.WaitGroup
	for i := range 32 {
		workers.Go(func() {
			ticket, err := replicas[i%2].ReserveCredits(context.Background(), provider, fmt.Sprintf("race-%d", i),
				time.Now().UTC(), 10, 100, 100)
			if err == nil {
				accepted <- ticket
			} else if !errors.Is(err, domain.ErrEmbeddingQuotaExceeded) {
				failures <- err
			}
		})
	}
	workers.Wait()
	close(accepted)
	close(failures)
	for err := range failures {
		t.Error(err)
	}
	if t.Failed() {
		t.FailNow()
	}
	tickets := make([]domusage.CreditTicket, 0, 10)
	for ticket := range accepted {
		tickets = append(tickets, ticket)
	}
	if len(tickets) > 10 {
		t.Fatalf("over-admitted %d invocations", len(tickets))
	}
	for len(tickets) < 10 {
		tickets = append(tickets, reserveCredit(t, replicas[0], provider, fmt.Sprintf("fill-%d", len(tickets)), 10, 100))
	}
	_, err := replicas[1].ReserveCredits(context.Background(), provider, "denied", time.Now().UTC(), 10, 100, 100)
	if !errors.Is(err, domain.ErrEmbeddingQuotaExceeded) {
		t.Fatalf("shared limit exceeded: %v", err)
	}
	for i := range tickets {
		completeCredit(t, replicas[i%2], &tickets[i], 3, false)
	}
	view, err := NewAccounting(store).Snapshot(context.Background(), provider, time.Now().UTC())
	if err != nil || view.Day != (domusage.Counters{Tokens: 30, Requests: 10}) {
		t.Fatalf("peer pressure/restart lost or duplicated usage: %+v %v", view, err)
	}
	tail := reserveCredit(t, replicas[1], provider, "reused-surplus", 70, 100)
	completeCredit(t, replicas[0], &tail, 70, false)
	_, err = replicas[0].ReserveCredits(context.Background(), provider, "exhausted", time.Now().UTC(), 1, 100, 100)
	if !errors.Is(err, domain.ErrEmbeddingQuotaExceeded) {
		t.Fatalf("late refunds returned spent credits: %v", err)
	}
}

func TestLeasedCreditRestartFencesOldCacheAndKeepsUnknownReservationLive(t *testing.T) {
	store, _ := creditRepositoryFixture(t)
	provider := "leased-restart-" + rand.Text()
	original := NewAccounting(store)
	ticket := reserveCredit(t, original, provider, "inflight", 40, 1280)
	restarted := NewAccounting(store)
	afterExpiry := time.UnixMilli(ticket.ExpiresAt + 1)
	if err := restarted.ReconcileCredits(context.Background(), provider, afterExpiry, 32); err != nil {
		t.Fatal(err)
	}
	view, err := restarted.Snapshot(context.Background(), provider, time.Now().UTC())
	if err != nil || view.Day != (domusage.Counters{Reserved: 40, Requests: 1, Unknown: 1}) {
		t.Fatalf("restart released ambiguous consumption: %+v %v", view, err)
	}
	fresh := reserveCredit(t, original, provider, "after-recovery", 10, 1280)
	if fresh.GrantID == ticket.GrantID {
		t.Fatal("old cached grant admitted work after recovery")
	}
	completeCredit(t, restarted, &ticket, 7, false)
	completeCredit(t, restarted, &ticket, 7, false)
	completeCredit(t, original, &fresh, 2, true)
	view, err = restarted.Snapshot(context.Background(), provider, time.Now().UTC())
	if err != nil || view.Day != (domusage.Counters{Tokens: 9, Reserved: 8, Requests: 2, Unknown: 1}) {
		t.Fatalf("late reply did not reconcile original reservation: %+v %v", view, err)
	}
}

func TestLeasedCreditBootstrapsLegacyAndOrdinaryHistoryLive(t *testing.T) {
	store, _ := creditRepositoryFixture(t)
	provider, now := "leased-history-"+rand.Text(), time.Now().UTC()
	repo := NewAccounting(store)
	if _, err := repo.Reserve(context.Background(), provider, "legacy", now, 20, 100, 100); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Complete(context.Background(), provider, "legacy", now, 13, false); err != nil {
		t.Fatal(err)
	}
	if err := repo.ReserveUsage(context.Background(), provider, "ordinary", now); err != nil {
		t.Fatal(err)
	}
	if err := repo.CompleteUsage(context.Background(), provider, "ordinary", now, 7, false); err != nil {
		t.Fatal(err)
	}
	ticket := reserveCredit(t, repo, provider, "strict", 80, 100)
	completeCredit(t, repo, &ticket, 80, false)
	_, err := NewAccounting(store).ReserveCredits(context.Background(), provider, "over", now, 1, 100, 100)
	if !errors.Is(err, domain.ErrEmbeddingQuotaExceeded) {
		t.Fatalf("history omitted from admission: %v", err)
	}
	view, err := repo.Snapshot(context.Background(), provider, now)
	if err != nil || view.Day != (domusage.Counters{Tokens: 100, Requests: 3}) {
		t.Fatalf("merged history: %+v %v", view, err)
	}
}

func TestLeasedCreditUnknownPriorHistoryExhaustsOnlyItsPeriodsLive(t *testing.T) {
	store, _ := creditRepositoryFixture(t)
	provider, now := "leased-unknown-history-"+rand.Text(), time.Now().UTC()
	repo := NewAccounting(store)
	if err := repo.ReserveUsage(context.Background(), provider, "unbounded-unknown", now); err != nil {
		t.Fatal(err)
	}
	if err := repo.CompleteUsage(context.Background(), provider, "unbounded-unknown", now, 3, true); err != nil {
		t.Fatal(err)
	}
	_, err := repo.ReserveCredits(context.Background(), provider, "strict", now, 10, 100, 100)
	if !errors.Is(err, domain.ErrEmbeddingQuotaExceeded) {
		t.Fatalf("unknown history admitted strict spending: %v", err)
	}
	view, err := NewAccounting(store).Snapshot(context.Background(), provider, now)
	want := domusage.Counters{Tokens: 3, Reserved: 97, Requests: 1, Unknown: 1}
	if err != nil || view.Day != want || view.Month != want || view.Total.Reserved != 0 {
		t.Fatalf("unknown baseline was lost or charged lifetime usage: %+v %v", view, err)
	}
}

func TestLeasedCreditPartialInstallationRecoversAfterProcessLossLive(t *testing.T) {
	store, _ := creditRepositoryFixture(t)
	store.failInstallAt = 3
	provider, now := "leased-partial-"+rand.Text(), time.Now().UTC()
	_, err := NewAccounting(store).ReserveCredits(context.Background(), provider, "interrupted", now, 40, 1280, 1280)
	if err == nil {
		t.Fatal("interrupted installer admitted an upstream call")
	}
	control := creditControl(provider, "", now)
	active, err := store.CatalogIDs(context.Background(), control.ActiveKey, "", 32)
	if err != nil || len(active) != 0 {
		t.Fatalf("partially installed grant was published for pressure recovery: %v %v", active, err)
	}
	restarted := NewAccounting(store)
	afterExpiry := now.Add(creditLeaseLifetime + time.Second)
	if err := restarted.ReconcileCredits(context.Background(), provider, afterExpiry, 32); err != nil {
		t.Fatal(err)
	}
	view, err := restarted.Snapshot(context.Background(), provider, now)
	if err != nil || view != (domusage.Snapshot{}) {
		t.Fatalf("unstarted request became billable usage: %+v %v", view, err)
	}
	ticket := reserveCredit(t, restarted, provider, "all-capacity-returned", 1280, 1280)
	completeCredit(t, restarted, &ticket, 0, false)
}

func TestLeasedCreditCompletionKeepsOriginalUTCPeriodsLive(t *testing.T) {
	store, _ := creditRepositoryFixture(t)
	provider, now := "leased-periods-"+rand.Text(), time.Now().UTC()
	previous := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC).Add(-time.Second)
	c := creditControl(provider, "prior-month", previous)
	c.Action, c.Wanted, c.Minimum, c.DailyLimit, c.MonthlyLimit = "grant", 10, 10, 100, 100
	c.Partitions, c.Grant.ExpiresAt = []int{17}, now.Add(time.Minute).UnixMilli()
	grant, err := store.CreditGrant(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	ticket := domusage.CreditTicket{Provider: provider, ID: "cross-midnight", GrantID: grant.Grant.ID,
		At: previous.UnixMilli(), ExpiresAt: grant.Grant.ExpiresAt, Partition: 17,
		Amount: 10, DailyLimit: 100, MonthlyLimit: 100}
	pool := creditPoolCommand(&ticket, "install")
	pool.Capacity = 10
	if _, err := store.CreditPool(context.Background(), pool); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreditPool(context.Background(), creditPoolCommand(&ticket, "reserve")); err != nil {
		t.Fatal(err)
	}
	restarted := NewAccounting(store)
	completeCredit(t, restarted, &ticket, 4, false)
	view, err := restarted.Snapshot(context.Background(), provider, now)
	if err != nil || view.Day != (domusage.Counters{}) || view.Month != (domusage.Counters{}) ||
		view.Total != (domusage.Counters{Tokens: 4, Requests: 1}) {
		t.Fatalf("completion charged the new UTC periods: %+v %v", view, err)
	}
}

func TestLeasedCreditLostActivationReplyCancelsUnstartedUsageLive(t *testing.T) {
	store, _ := creditRepositoryFixture(t)
	store.failActivateAt = 1
	provider, now := "leased-lost-activation-"+rand.Text(), time.Now().UTC()
	_, err := NewAccounting(store).ReserveCredits(context.Background(), provider, "not-started", now, 40, 1280, 1280)
	if err == nil {
		t.Fatal("failed activation admitted upstream work")
	}
	view, err := NewAccounting(store).Snapshot(context.Background(), provider, now)
	if err != nil || view != (domusage.Snapshot{}) {
		t.Fatalf("unstarted invocation counted as usage: %+v %v", view, err)
	}
	restarted := NewAccounting(store)
	ticket := reserveCredit(t, restarted, provider, "full-capacity-reused", 1280, 1280)
	completeCredit(t, restarted, &ticket, 0, false)
}

func TestLeasedCreditDuplicateCompletionSurvivesSettledManifestExpiryLive(t *testing.T) {
	store, _ := creditRepositoryFixture(t)
	repo, provider := NewAccounting(store), "leased-settled-expiry-"+rand.Text()
	ticket := reserveCredit(t, repo, provider, "original", 40, 1280)
	if err := repo.ReconcileCredits(context.Background(), provider, time.UnixMilli(ticket.ExpiresAt+1), 32); err != nil {
		t.Fatal(err)
	}
	completeCredit(t, repo, &ticket, 7, false)
	control := creditControl(provider, ticket.GrantID, time.UnixMilli(ticket.At))
	// Model the shorter settled-manifest TTL while the partition receipt lives.
	if err := store.Del(context.Background(), control.GrantKey); err != nil {
		t.Fatal(err)
	}
	completeCredit(t, NewAccounting(store), &ticket, 7, false)
	view, err := repo.Snapshot(context.Background(), provider, time.Now().UTC())
	if err != nil || view.Day != (domusage.Counters{Tokens: 7, Requests: 1}) {
		t.Fatalf("expired control receipt broke invocation idempotence: %+v %v", view, err)
	}
	fields, err := store.HGetAll(context.Background(), control.DayKey)
	if err != nil || fields["booked"] != "7" {
		t.Fatalf("settled control expiry returned spent credit: %v %v", fields, err)
	}
}
