package valkey

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kailas-cloud/vecdex/internal/db"
	domusage "github.com/kailas-cloud/vecdex/internal/domain/usage"
)

func creditPoolFixture(t *testing.T) (*Store, Config, *db.CreditPoolCommand) {
	t.Helper()
	cfg := Config{Standalone: true, Addrs: []string{os.Getenv("VECDEX_TEST_VALKEY_ADDR")}}
	if seeds := os.Getenv("VECDEX_E2E_VALKEY_CLUSTER_ADDRS"); seeds != "" {
		cfg.Standalone, cfg.Addrs = false, strings.Split(seeds, ",")
	} else if cfg.Addrs[0] == "" {
		t.Skip("isolated Valkey fixture required")
	}
	store, err := NewStore(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	base := "vecdex:v3:credit-test:{" + rand.Text() + "}"
	c := &db.CreditPoolCommand{Action: "install", GrantID: rand.Text(), PoolKey: base + ":pool",
		LiveKey: base + ":live", CounterKeys: []string{base + ":day", base + ":month", base + ":total"},
		Capacity: 100, ExpiresAt: time.Now().Add(time.Minute).UnixMilli()}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		keys := append([]string{c.PoolKey, c.LiveKey}, c.CounterKeys...)
		for i := range 128 {
			keys = append(keys, fmt.Sprintf("%s:request:%d", base, i))
		}
		for _, key := range keys {
			if err := store.Del(ctx, key); err != nil {
				t.Error(err)
			}
		}
	})
	return store, cfg, c
}

func creditTestReceipt(c *db.CreditPoolCommand, i int) string {
	return strings.TrimSuffix(c.PoolKey, ":pool") + fmt.Sprintf(":request:%d", i)
}

func nativeCreditPool(t *testing.T, store *Store, c *db.CreditPoolCommand) db.CreditPoolResult {
	t.Helper()
	result, err := store.CreditPool(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func assertNativeCreditUsage(t *testing.T, store *Store, c *db.CreditPoolCommand, want domusage.Counters) {
	t.Helper()
	snapshots, err := store.BudgetSnapshots(context.Background(), [][]string{c.CounterKeys})
	if err != nil || len(snapshots) != 1 || snapshots[0].Day != want || snapshots[0].Month != want ||
		snapshots[0].Total != want {
		t.Fatalf("native credit usage: %+v, want %+v, %v", snapshots, want, err)
	}
}

func TestCreditPoolLateCompletionAfterClientRestartLive(t *testing.T) {
	store, cfg, command := creditPoolFixture(t)
	nativeCreditPool(t, store, command)
	command.Action, command.Amount, command.ReceiptKey = "reserve", 40, creditTestReceipt(command, 0)
	nativeCreditPool(t, store, command)
	if result := nativeCreditPool(t, store, command); result.Applied {
		t.Fatal("reservation replay charged twice")
	}
	command.Action = "close"
	closed := nativeCreditPool(t, store, command)
	if !closed.Closed || closed.Available != 60 || closed.Pending != 1 {
		t.Fatalf("unsafe close: %+v", closed)
	}
	assertNativeCreditUsage(t, store, command, domusage.Counters{Reserved: 40, Requests: 1, Unknown: 1})
	restarted, err := NewStore(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	command.Action, command.Tokens = "complete", 7
	result := nativeCreditPool(t, restarted, command)
	if result.Available != 93 || result.Pending != 0 || result.Refund != 33 || !result.Applied {
		t.Fatalf("late reply did not settle the original reservation: %+v", result)
	}
	if replay := nativeCreditPool(t, store, command); replay.Applied || replay.Refund != 0 || replay.Available != 93 {
		t.Fatalf("duplicate reply returned credits twice: %+v", replay)
	}
	assertNativeCreditUsage(t, store, command, domusage.Counters{Tokens: 7, Requests: 1})
	command.Action = "reserve"
	if _, err := store.CreditPool(context.Background(), command); !errors.Is(err, db.ErrCreditPoolClosed) {
		t.Fatalf("closed pool admitted work: %v", err)
	}
}

func TestCreditPoolRecoveryFencesUninstalledPoolLive(t *testing.T) {
	store, _, command := creditPoolFixture(t)
	command.Action = "close"
	closed := nativeCreditPool(t, store, command)
	if !closed.Closed || closed.Available != 100 || closed.Pending != 0 {
		t.Fatalf("recovery: %+v", closed)
	}
	command.Action = "install"
	if _, err := store.CreditPool(context.Background(), command); !errors.Is(err, db.ErrCreditPoolClosed) {
		t.Fatalf("late installer reopened a recovered pool: %v", err)
	}
	assertNativeCreditUsage(t, store, command, domusage.Counters{})
}

func TestCreditPoolConcurrentAdmissionAndRecoveryLive(t *testing.T) {
	store, _, command := creditPoolFixture(t)
	nativeCreditPool(t, store, command)
	accepted := make(chan int, 64)
	failures := make(chan error, 64)
	var workers sync.WaitGroup
	for i := range 64 {
		workers.Go(func() {
			c := *command
			c.Action, c.Amount, c.ReceiptKey = "reserve", 10, creditTestReceipt(&c, i)
			if _, err := store.CreditPool(context.Background(), &c); err == nil {
				accepted <- i
			} else if !errors.Is(err, db.ErrCreditPoolEmpty) {
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
	if len(accepted) != 10 || t.Failed() {
		t.Fatalf("admitted %d, expected exactly 10", len(accepted))
	}
	command.Action = "close"
	nativeCreditPool(t, store, command)
	assertNativeCreditUsage(t, store, command, domusage.Counters{Reserved: 100, Requests: 10, Unknown: 10})
	for i := range accepted {
		workers.Go(func() {
			c := *command
			c.Action, c.Tokens, c.ReceiptKey = "complete", 7, creditTestReceipt(&c, i)
			for range 2 {
				nativeCreditPool(t, store, &c)
			}
		})
	}
	workers.Wait()
	closed := nativeCreditPool(t, store, command)
	if !closed.Closed || closed.Available != 30 || closed.Pending != 0 {
		t.Fatalf("recovered pool: %+v", closed)
	}
	assertNativeCreditUsage(t, store, command, domusage.Counters{Tokens: 70, Requests: 10})
}

func TestCreditPoolLeaseExpiryFencesWatchedReservationLive(t *testing.T) {
	store, _, command := creditPoolFixture(t)
	// Leave time to prepare WATCH on a remote fixture; host/VM clocks may differ
	// by a few hundred milliseconds. The assertion still waits for lease expiry.
	command.ExpiresAt = time.Now().Add(2 * time.Second).UnixMilli()
	nativeCreditPool(t, store, command)
	command.Action, command.Amount, command.ReceiptKey = "reserve", 40, creditTestReceipt(command, 0)
	client, release := store.client.Dedicate()
	defer release()
	defer unwatch(client)
	keys := append([]string{command.PoolKey, command.LiveKey, command.ReceiptKey}, command.CounterKeys...)
	if err := watchKeys(context.Background(), client, keys...); err != nil {
		t.Fatal(err)
	}
	state, live, err := readCreditPool(context.Background(), client, command)
	if err != nil {
		t.Fatal(err)
	}
	if err := applyCreditPool(command, &state, live, time.Now().UnixMilli()); err != nil {
		t.Fatal(err)
	}
	commands, err := creditPoolCommands(client, command, &state)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Until(time.UnixMilli(command.ExpiresAt)) + time.Millisecond)
	_, committed, err := execTransaction(context.Background(), client, commands)
	if err != nil || committed {
		t.Fatalf("expired guard permitted prepared reservation: %v %v", committed, err)
	}
	if _, err := store.CreditPool(context.Background(), command); !errors.Is(err, db.ErrCreditPoolClosed) {
		t.Fatalf("expired pool admitted a fresh reservation: %v", err)
	}
	command.Action = "close"
	if result := nativeCreditPool(t, store, command); result.Available != 100 {
		t.Fatalf("aborted reservation spent credit: %+v", result)
	}
	assertNativeCreditUsage(t, store, command, domusage.Counters{})
}

func TestCreditPoolExpiryUnknownAndMalformedLedgerLive(t *testing.T) {
	store, _, command := creditPoolFixture(t)
	nativeCreditPool(t, store, command)
	command.Action, command.Amount, command.ReceiptKey = "reserve", 40, creditTestReceipt(command, 0)
	nativeCreditPool(t, store, command)
	if err := store.Del(context.Background(), command.LiveKey); err != nil {
		t.Fatal(err)
	}
	command.Action, command.Tokens, command.Unknown = "complete", 7, true
	result := nativeCreditPool(t, store, command)
	if !result.Closed || result.Available != 60 || result.Pending != 0 || result.Refund != 0 {
		t.Fatalf("expired unknown request released credit: %+v", result)
	}
	assertNativeCreditUsage(t, store, command, domusage.Counters{Tokens: 7, Reserved: 33, Requests: 1, Unknown: 1})
	command.Action, command.ReceiptKey = "close", ""
	if err := store.Del(context.Background(), command.CounterKeys[0]); err != nil {
		t.Fatal(err)
	}
	broken := store.b().Set().Key(command.CounterKeys[0]).Value("broken").Build()
	if err := store.do(context.Background(), broken).Error(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreditPool(context.Background(), command); err == nil {
		t.Fatal("malformed ledger was treated as zero")
	}
	read := store.b().Get().Key(creditTestReceipt(command, 0)).Build()
	if receipt, err := store.do(context.Background(), read).ToString(); err != nil || receipt == "" {
		t.Fatalf("failed recovery removed the invocation receipt: %s %v", receipt, err)
	}
}

func TestCreditPoolConcurrentCloseAndCompletionLive(t *testing.T) {
	store, _, command := creditPoolFixture(t)
	command.Capacity = 640
	nativeCreditPool(t, store, command)
	for i := range 64 {
		c := *command
		c.Action, c.Amount, c.ReceiptKey = "reserve", 10, creditTestReceipt(&c, i)
		nativeCreditPool(t, store, &c)
	}
	var workers sync.WaitGroup
	for i := range 64 {
		workers.Go(func() {
			c := *command
			c.Action, c.Tokens, c.ReceiptKey = "complete", 3, creditTestReceipt(&c, i)
			if i%7 == 0 {
				c.Tokens, c.Unknown = 1, true
			}
			for range 2 {
				nativeCreditPool(t, store, &c)
			}
		})
		if i%2 == 0 {
			workers.Go(func() {
				c := *command
				c.Action = "close"
				nativeCreditPool(t, store, &c)
			})
		}
	}
	workers.Wait()
	command.Action = "close"
	closed := nativeCreditPool(t, store, command)
	if !closed.Closed || closed.Pending != 0 || closed.Available != 378 {
		t.Fatalf("close racing completions lost or released credit: %+v", closed)
	}
	assertNativeCreditUsage(t, store, command, domusage.Counters{Tokens: 172, Reserved: 90, Requests: 64, Unknown: 10})
}

func TestCreditPoolChangedReadsRetryBeforeReportingLostReservationLive(t *testing.T) {
	store, _, command := creditPoolFixture(t)
	nativeCreditPool(t, store, command)
	command.Action, command.Amount, command.ReceiptKey = "reserve", 40, creditTestReceipt(command, 0)
	nativeCreditPool(t, store, command)
	other := *command
	other.ReceiptKey = creditTestReceipt(command, 1)
	nativeCreditPool(t, store, &other)
	client, release := store.client.Dedicate()
	defer release()
	defer unwatch(client)
	keys := append([]string{command.PoolKey, command.LiveKey, command.ReceiptKey}, command.CounterKeys...)
	ctx := context.Background()
	if err := watchKeys(ctx, client, keys...); err != nil {
		t.Fatal(err)
	}
	state, live, err := readCreditPool(ctx, client, command)
	if err != nil {
		t.Fatal(err)
	}
	other.Action, other.Tokens = "complete", 7
	nativeCreditPool(t, store, &other)
	state.counters, state.receipt, err = readCreditCounters(ctx, client, command)
	if err != nil {
		t.Fatal(err)
	}
	command.Action, command.Tokens = "complete", 7
	readErr := applyCreditPool(command, &state, live, time.Now().UnixMilli())
	if readErr == nil {
		t.Fatal("fixture did not produce mixed versions")
	}
	consistent, err := validateCreditReadError(ctx, client, readErr)
	if consistent || err != nil {
		t.Fatalf("mixed versions were treated as lost reservations: %v %v", consistent, err)
	}
	nativeCreditPool(t, store, command)
	assertNativeCreditUsage(t, store, command, domusage.Counters{Tokens: 14, Requests: 2})
}

func TestCreditPoolLatePeriodActivityKeepsReconciliationHistoryLive(t *testing.T) {
	store, _, command := creditPoolFixture(t)
	nativeCreditPool(t, store, command)
	command.Action, command.Amount, command.ReceiptKey = "reserve", 40, creditTestReceipt(command, 0)
	nativeCreditPool(t, store, command)
	shortenCreditHistory(t, store, command.CounterKeys[:2])
	command.Action, command.Tokens = "complete", 7
	nativeCreditPool(t, store, command)
	assertCreditHistoryRetention(t, store, command.CounterKeys[:2])
	assertNativeCreditUsage(t, store, command, domusage.Counters{Tokens: 7, Requests: 1})
}

func shortenCreditHistory(t *testing.T, store *Store, keys []string) {
	t.Helper()
	for _, key := range keys {
		if err := store.Expire(context.Background(), key, 2*time.Second, false); err != nil {
			t.Fatal(err)
		}
	}
}

func assertCreditHistoryRetention(t *testing.T, store *Store, keys []string) {
	t.Helper()
	for _, key := range keys {
		remaining, err := store.do(context.Background(), store.b().Pttl().Key(key).Build()).AsInt64()
		if err != nil || remaining < creditReceiptLifetime.Milliseconds() {
			t.Fatalf("period created earlier cannot retain late invocation receipts: ttl=%d %v", remaining, err)
		}
	}
}

var _ interface {
	CreditPool(context.Context, *db.CreditPoolCommand) (db.CreditPoolResult, error)
} = (*Store)(nil)
