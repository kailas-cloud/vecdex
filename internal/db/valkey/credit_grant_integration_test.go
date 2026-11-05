package valkey

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kailas-cloud/vecdex/internal/db"
	"github.com/kailas-cloud/vecdex/internal/domain"
	domusage "github.com/kailas-cloud/vecdex/internal/domain/usage"
)

func creditGrantFixture(t *testing.T) (*Store, Config, *db.CreditGrantCommand) {
	t.Helper()
	store, cfg, pool := creditPoolFixture(t)
	base := strings.TrimSuffix(pool.PoolKey, ":pool")
	now := time.Now()
	c := &db.CreditGrantCommand{Action: "grant", DayKey: base + ":allocated-day", MonthKey: base + ":allocated-month",
		ActiveKey: base + ":active", DueKey: base + ":due", GrantKey: base + ":grant:0",
		Grant:      db.CreditGrant{ID: "grant-0", At: now.UnixMilli(), ExpiresAt: now.Add(time.Minute).UnixMilli()},
		Partitions: []int{1, 32, 63, 94, 125, 156, 187, 218}, Wanted: 100, Minimum: 10, DailyLimit: 100, MonthlyLimit: 100}
	t.Cleanup(func() {
		keys := make([]string, 0, 68)
		keys = append(keys, c.DayKey, c.MonthKey, c.ActiveKey, c.DueKey)
		for i := range 64 {
			keys = append(keys, fmt.Sprintf("%s:grant:%d", base, i))
		}
		for _, key := range keys {
			if err := store.Del(context.Background(), key); err != nil {
				t.Error(err)
			}
		}
	})
	return store, cfg, c
}

func nativeCreditGrant(t *testing.T, store *Store, c *db.CreditGrantCommand) db.CreditGrantResult {
	t.Helper()
	result, err := store.CreditGrant(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func assertCreditAllocation(t *testing.T, store *Store, c *db.CreditGrantCommand, want int64) {
	t.Helper()
	for _, key := range []string{c.DayKey, c.MonthKey} {
		fields, err := store.HGetAll(context.Background(), key)
		if err != nil || fields["booked"] != strconv.FormatInt(want, 10) {
			t.Fatalf("allocation %s: %v, want %d, %v", key, fields, want, err)
		}
	}
}

func TestCreditGrantConcurrentAllocationAndCumulativeRefundLive(t *testing.T) {
	store, _, command := creditGrantFixture(t)
	command.Wanted = 30
	accepted := make(chan db.CreditGrantCommand, 32)
	failures := make(chan error, 32)
	var workers sync.WaitGroup
	for i := range 32 {
		workers.Go(func() {
			c := *command
			c.Grant.ID, c.GrantKey = fmt.Sprintf("grant-%d", i), strings.TrimSuffix(c.GrantKey, ":0")+fmt.Sprintf(":%d", i)
			result, err := store.CreditGrant(context.Background(), &c)
			if err == nil {
				c.Grant = result.Grant
				accepted <- c
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
	if len(accepted) != 4 || t.Failed() {
		t.Fatalf("allocated %d grants, expected 4", len(accepted))
	}
	assertCreditAllocation(t, store, command, 100)
	var sum int64
	var first db.CreditGrantCommand
	for c := range accepted {
		sum += c.Grant.Amount
		replay := nativeCreditGrant(t, store, &c)
		if replay.Grant.Amount != c.Grant.Amount {
			t.Fatal("allocation retry changed its grant")
		}
		c.Action, c.Refund = "refund", c.Grant.Amount-3
		nativeCreditGrant(t, store, &c)
		nativeCreditGrant(t, store, &c)
		first = c
	}
	if sum != 100 {
		t.Fatalf("allocated %d, expected 100", sum)
	}
	assertCreditAllocation(t, store, command, 12)
	first.Refund++
	nativeCreditGrant(t, store, &first)
	first.Refund-- // An older worker reply must not return the same credit again.
	nativeCreditGrant(t, store, &first)
	assertCreditAllocation(t, store, command, 11)
	command.Grant.ID, command.GrantKey = "grant-63", strings.TrimSuffix(command.GrantKey, ":0")+":63"
	command.Wanted = 100
	if next := nativeCreditGrant(t, store, command); next.Grant.Amount != 89 {
		t.Fatalf("available grant: %+v", next)
	}
	assertCreditAllocation(t, store, command, 100)
}

func TestCreditGrantHistoryPolicyAndRegistryLive(t *testing.T) {
	store, _, command := creditGrantFixture(t)
	command.BaselineDay, command.BaselineMonth = 20, 20
	first := nativeCreditGrant(t, store, command)
	if first.Grant.Amount != 80 || len(first.Grant.Slices) != 8 {
		t.Fatalf("history forgotten: %+v", first)
	}
	assertCreditCatalogCount(t, store, command.ActiveKey, 0)
	command.Action = "activate"
	nativeCreditGrant(t, store, command)
	command.Action = "grant"
	for _, key := range []string{command.ActiveKey, command.DueKey} {
		if count, err := store.CatalogCount(context.Background(), key); err != nil || count != 1 {
			t.Fatalf("grant not registered for recovery: %s, %d, %v", key, count, err)
		}
	}
	command.DailyLimit = 101
	if _, err := store.CreditGrant(context.Background(), command); !errors.Is(err, db.ErrCreditPolicyChanged) {
		t.Fatalf("policy change silently bypassed accounting: %v", err)
	}
	command.DailyLimit, command.Action, command.Refund = 100, "refund", 80
	command.KeepDue, command.NextCheck = true, time.Now().Add(time.Second).UnixMilli()
	nativeCreditGrant(t, store, command)
	assertCreditAllocation(t, store, command, 20)
	if count, err := store.CatalogCount(context.Background(), command.ActiveKey); err != nil || count != 0 {
		t.Fatalf("closed grant stayed active: %d %v", count, err)
	}
	command.KeepDue = false
	nativeCreditGrant(t, store, command)
	if count, err := store.CatalogCount(context.Background(), command.DueKey); err != nil || count != 0 {
		t.Fatalf("settled grant stayed queued: %d %v", count, err)
	}
	command.Action = "read"
	read := nativeCreditGrant(t, store, command)
	if !read.Closed || read.Released != 80 || read.Grant.Amount != 80 {
		t.Fatalf("manifest: %+v", read)
	}
}

func assertCreditCatalogCount(t *testing.T, store *Store, key string, want int) {
	t.Helper()
	if count, err := store.CatalogCount(context.Background(), key); err != nil || count != want {
		t.Fatalf("credit catalog: got %d want %d, %v", count, want, err)
	}
}

func TestCreditGrantConcurrentRefundCannotReturnCreditsTwiceLive(t *testing.T) {
	store, _, command := creditGrantFixture(t)
	nativeCreditGrant(t, store, command)
	command.Action = "refund"
	var workers sync.WaitGroup
	for i := range 64 {
		workers.Go(func() {
			c := *command
			c.Refund = int64(99 - i)
			nativeCreditGrant(t, store, &c)
		})
	}
	workers.Wait()
	assertCreditAllocation(t, store, command, 1)
	command.Action = "read"
	if result := nativeCreditGrant(t, store, command); !result.Closed || result.Released != 99 {
		t.Fatalf("cumulative refund lost its maximum: %+v", result)
	}
}

func TestCreditGrantRecoveryAcrossSlotsLive(t *testing.T) {
	store, cfg, command := creditGrantFixture(t)
	command.Wanted, command.DailyLimit, command.MonthlyLimit = 800, 800, 800
	allocated := nativeCreditGrant(t, store, command)
	pools := make([]db.CreditPoolCommand, len(allocated.Grant.Slices))
	for i, slice := range allocated.Grant.Slices {
		base := fmt.Sprintf("vecdex:v3:credit-recovery-test:{%s:%d}", rand.Text(), slice.Partition)
		pools[i] = db.CreditPoolCommand{Action: "install", GrantID: allocated.Grant.ID,
			PoolKey: base + ":pool", LiveKey: base + ":live", ReceiptKey: base + ":receipt",
			CounterKeys: []string{base + ":day", base + ":month", base + ":total"},
			Capacity:    slice.Amount, ExpiresAt: allocated.Grant.ExpiresAt}
		pool := &pools[i]
		t.Cleanup(func() {
			for _, key := range append([]string{pool.PoolKey, pool.LiveKey, pool.ReceiptKey}, pool.CounterKeys...) {
				if err := store.Del(context.Background(), key); err != nil {
					t.Error(err)
				}
			}
		})
		if i < 3 {
			nativeCreditPool(t, store, pool)
		} // Simulate a crash during installation.
	}
	pools[0].Action, pools[0].Amount = "reserve", 40
	nativeCreditPool(t, store, &pools[0])
	restarted, err := NewStore(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	read := *command
	read.Action = "read"
	manifest := nativeCreditGrant(t, restarted, &read)
	if len(manifest.Grant.Slices) != 8 {
		t.Fatal("restart lost grant slices")
	}
	refund, pending := closeTestCreditPools(t, restarted, pools)
	if refund != 760 || pending != 1 {
		t.Fatalf("unsafe initial recovery: unused=%d pending=%d", refund, pending)
	}
	command.Action, command.Refund, command.KeepDue = "refund", refund, true
	command.NextCheck = time.Now().Add(time.Second).UnixMilli()
	nativeCreditGrant(t, restarted, command)
	assertCreditAllocation(t, store, command, 40)
	pools[7].Action = "install"
	if _, err := store.CreditPool(context.Background(), &pools[7]); !errors.Is(err, db.ErrCreditPoolClosed) {
		t.Fatalf("late installer reopened already returned credit: %v", err)
	}
	pools[0].Action, pools[0].Tokens = "complete", 7
	nativeCreditPool(t, store, &pools[0])
	refund, pending = closeTestCreditPools(t, restarted, pools)
	if refund != 793 || pending != 0 {
		t.Fatalf("late reply not recovered: %d %d", refund, pending)
	}
	command.Refund, command.KeepDue = refund, false
	nativeCreditGrant(t, restarted, command)
	command.Refund = 760
	nativeCreditGrant(t, store, command)
	assertCreditAllocation(t, store, command, 7)
	assertNativeCreditUsage(t, store, &pools[0], domusage.Counters{Tokens: 7, Requests: 1})
}

func closeTestCreditPools(t *testing.T, store *Store, pools []db.CreditPoolCommand) (refund, pending int64) {
	t.Helper()
	for i := range pools {
		pool := pools[i]
		pool.Action = "close"
		result := nativeCreditPool(t, store, &pool)
		refund += result.Available
		pending += result.Pending
	}
	return refund, pending
}

func TestCreditGrantLatePeriodActivityKeepsAllocationHistoryLive(t *testing.T) {
	store, _, command := creditGrantFixture(t)
	command.Wanted = 30
	nativeCreditGrant(t, store, command)
	shortenCreditHistory(t, store, []string{command.DayKey, command.MonthKey})
	command.Grant.ID, command.GrantKey = "grant-1", strings.TrimSuffix(command.GrantKey, ":0")+":1"
	nativeCreditGrant(t, store, command)
	assertCreditHistoryRetention(t, store, []string{command.DayKey, command.MonthKey})
	assertCreditAllocation(t, store, command, 60)
}

func TestCreditGrantSettledControlMetadataHasBoundedRetentionLive(t *testing.T) {
	store, _, command := creditGrantFixture(t)
	nativeCreditGrant(t, store, command)
	command.Action, command.Refund = "refund", 100
	nativeCreditGrant(t, store, command)
	remaining, err := store.do(context.Background(), store.b().Pttl().Key(command.GrantKey).Build()).AsInt64()
	if err != nil || remaining <= 0 || remaining > creditSettledGrantLifetime.Milliseconds() {
		t.Fatalf("settled manifest retains unnecessary control-slot memory: %d %v", remaining, err)
	}
	assertCreditHistoryRetention(t, store, []string{command.DayKey, command.MonthKey})
}
