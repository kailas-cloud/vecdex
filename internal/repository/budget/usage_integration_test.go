package budget

import (
	"context"
	"crypto/rand"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kailas-cloud/vecdex/internal/db/valkey"
	domusage "github.com/kailas-cloud/vecdex/internal/domain/usage"
	"github.com/redis/rueidis"
)

func usageFixture(t *testing.T) (*valkey.Store, valkey.Config) {
	t.Helper()
	cfg := valkey.Config{Standalone: true, Addrs: []string{os.Getenv("VECDEX_TEST_VALKEY_ADDR")}}
	if seeds := os.Getenv("VECDEX_E2E_VALKEY_CLUSTER_ADDRS"); seeds != "" {
		cfg.Standalone, cfg.Addrs = false, strings.Split(seeds, ",")
	} else if cfg.Addrs[0] == "" {
		t.Skip("isolated Valkey fixture required")
	}
	store, err := valkey.NewStore(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	return store, cfg
}

func TestPartitionedUsageAcrossPrimariesLive(t *testing.T) {
	store, cfg := usageFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	provider, now := "partitioned-"+rand.Text(), time.Now().UTC()
	repository := NewAccounting(store)
	const invocations = 512
	ids := make([]string, invocations)
	for i := range ids {
		ids[i] = fmt.Sprintf("invocation-%d", i)
	}
	defer cleanupUsageKeys(t, store, provider, ids, now)
	writeConcurrentUsage(t, ctx, repository, provider, ids, now)
	unknown := int64((invocations-1)/17 + 1)
	want := domusage.Counters{Requests: invocations, Tokens: (invocations - unknown) * 7, Unknown: unknown}
	started := time.Now()
	snapshot, err := repository.Snapshot(ctx, provider, now)
	if err != nil || snapshot.Day != want || snapshot.Month != want || snapshot.Total != want {
		t.Fatalf("sharded usage lost a receipt or counted a duplicate: %+v want %+v, %v", snapshot, want, err)
	}
	t.Logf("authoritative snapshot: %s for %d invocations", time.Since(started), invocations)
	legacy, err := store.HGetAll(ctx, accountingKeys(provider, "", now)[2])
	if err != nil || len(legacy) != 0 {
		t.Fatalf("ordinary usage touched the shared legacy counter: %v %v", legacy, err)
	}
	groups := make([][]string, UsagePartitionCount)
	for p := range groups {
		groups[p] = usageKeys(provider, "", p, now)[:3]
	}
	partitions, err := store.BudgetSnapshots(ctx, groups)
	if err != nil {
		t.Fatal(err)
	}
	populated := 0
	for _, partition := range partitions {
		if partition.Total.Requests > 0 {
			populated++
		}
	}
	if populated < 200 {
		t.Fatalf("only %d/256 usage partitions were used", populated)
	}
	t.Logf("populated usage partitions: %d/256", populated)
	if !cfg.Standalone {
		assertUsagePrimaryDistribution(t, ctx, cfg, groups, partitions)
	}
}

func writeConcurrentUsage(
	t *testing.T, ctx context.Context, repo *Accounting, provider string, ids []string, now time.Time,
) {
	t.Helper()
	jobs := make(chan int, len(ids))
	failures := make(chan error, len(ids))
	for i := range ids {
		jobs <- i
	}
	close(jobs)
	var workers sync.WaitGroup
	for range 16 {
		workers.Go(func() {
			for i := range jobs {
				if err := repo.ReserveUsage(ctx, provider, ids[i], now); err != nil {
					failures <- err
					continue
				}
				unknown, tokens := i%17 == 0, int64(7)
				if unknown {
					tokens = 0
				}
				for range 2 {
					if err := repo.CompleteUsage(ctx, provider, ids[i], now, tokens, unknown); err != nil {
						failures <- err
						break
					}
				}
			}
		})
	}
	workers.Wait()
	close(failures)
	for err := range failures {
		t.Error(err)
	}
	if t.Failed() {
		t.FailNow()
	}
}

func cleanupUsageKeys(t *testing.T, store *valkey.Store, provider string, ids []string, now time.Time) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	keys := make(chan string, UsagePartitionCount*3+len(ids)*2+3)
	for p := range UsagePartitionCount {
		for _, key := range usageKeys(provider, "", p, now)[:3] {
			keys <- key
		}
	}
	for _, id := range ids {
		keys <- usageKeys(provider, id, usagePartition(id), now)[3]
		keys <- accountingKeys(provider, id, now)[3]
	}
	for _, key := range accountingKeys(provider, "", now)[:3] {
		keys <- key
	}
	close(keys)
	var workers sync.WaitGroup
	for range 16 {
		workers.Go(func() {
			for key := range keys {
				if err := store.Del(ctx, key); err != nil {
					t.Error(err)
				}
			}
		})
	}
	workers.Wait()
}

func TestPartitionedUsageIncludesLegacyReservationHistoryLive(t *testing.T) {
	store, _ := usageFixture(t)
	provider, now := "history-"+rand.Text(), time.Now().UTC()
	ids := []string{"legacy-known", "legacy-unknown", "new-known", "new-unknown"}
	defer cleanupUsageKeys(t, store, provider, ids, now)
	repo := NewAccounting(store)
	ctx := context.Background()
	for _, id := range ids[:2] {
		if _, err := repo.Reserve(ctx, provider, id, now, 10, 100, 100); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := repo.Complete(ctx, provider, ids[0], now, 3, false); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Complete(ctx, provider, ids[1], now, 0, true); err != nil {
		t.Fatal(err)
	}
	for i, id := range ids[2:] {
		if err := repo.ReserveUsage(ctx, provider, id, now); err != nil {
			t.Fatal(err)
		}
		tokens := int64(7)
		if i == 1 {
			tokens = 0
		}
		if err := repo.CompleteUsage(ctx, provider, id, now, tokens, i == 1); err != nil {
			t.Fatal(err)
		}
	}
	snapshot, err := repo.Snapshot(ctx, provider, now)
	want := domusage.Counters{Tokens: 10, Requests: 4, Unknown: 2, Reserved: 10}
	if err != nil || snapshot.Day != want || snapshot.Month != want || snapshot.Total != want {
		t.Fatalf("legacy history or conservative unknown reservation lost: %+v want %+v, %v", snapshot, want, err)
	}
}

type usageSlotRange struct {
	first, last int64
	owner       string
}

func assertUsagePrimaryDistribution(
	t *testing.T, ctx context.Context, cfg valkey.Config, groups [][]string, partitions []domusage.Snapshot,
) {
	t.Helper()
	client, err := rueidis.NewClient(rueidis.ClientOption{InitAddress: cfg.Addrs, DisableCache: true})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	rows, err := client.Do(ctx, client.B().Arbitrary("CLUSTER").Args("SLOTS").Build()).ToArray()
	if err != nil {
		t.Fatal(err)
	}
	ranges := make([]usageSlotRange, 0, len(rows))
	for _, row := range rows {
		interval, err := decodeUsageSlotRange(row)
		if err != nil {
			t.Fatal(err)
		}
		ranges = append(ranges, interval)
	}
	counts := map[string]int64{}
	for i, group := range groups {
		if partitions[i].Total.Requests == 0 {
			continue
		}
		command := client.B().Get().Key(group[0]).Build()
		slot := int64(command.Slot())
		owner, err := usageSlotOwner(ranges, slot)
		if err != nil {
			t.Fatal(err)
		}
		counts[owner] += partitions[i].Total.Requests
	}
	if len(counts) < 3 {
		t.Fatalf("usage did not reach three primary owners: %v", counts)
	}
	for _, count := range counts {
		if count > 256 {
			t.Fatalf("one primary received more than half of usage: %v", counts)
		}
	}
	t.Logf("measured requests by physical primary: %v", counts)
}

func decodeUsageSlotRange(row rueidis.RedisMessage) (usageSlotRange, error) {
	values, err := row.ToArray()
	if err != nil || len(values) < 3 {
		return usageSlotRange{}, fmt.Errorf("invalid cluster slot inventory")
	}
	first, err := values[0].AsInt64()
	if err != nil {
		return usageSlotRange{}, fmt.Errorf("invalid first slot: %w", err)
	}
	last, err := values[1].AsInt64()
	if err != nil || first < 0 || first > last || last >= 16384 {
		return usageSlotRange{}, fmt.Errorf("invalid last slot")
	}
	primary, err := values[2].ToArray()
	if err != nil || len(primary) < 3 {
		return usageSlotRange{}, fmt.Errorf("invalid primary owner")
	}
	owner, err := primary[2].ToString()
	if err != nil || owner == "" {
		return usageSlotRange{}, fmt.Errorf("missing primary identity")
	}
	return usageSlotRange{first, last, owner}, nil
}

func usageSlotOwner(ranges []usageSlotRange, slot int64) (string, error) {
	for _, interval := range ranges {
		if slot >= interval.first && slot <= interval.last {
			return interval.owner, nil
		}
	}
	return "", fmt.Errorf("no physical owner for usage slot %d", slot)
}

func TestPartitionedUsageSurvivesRestartAndRolloverLive(t *testing.T) {
	store, cfg := usageFixture(t)
	provider, now := "rollover-"+rand.Text(), time.Now().UTC()
	previous := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC).Add(-time.Second)
	ids := []string{"cross-midnight"}
	defer cleanupUsageKeys(t, store, provider, ids, previous)
	repository := NewAccounting(store)
	if err := repository.ReserveUsage(context.Background(), provider, ids[0], previous); err != nil {
		t.Fatal(err)
	}
	restarted, err := valkey.NewStore(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	repository = NewAccounting(restarted)
	for range 2 {
		if err := repository.CompleteUsage(context.Background(), provider, ids[0], previous, 4, false); err != nil {
			t.Fatal(err)
		}
	}
	snapshot, err := repository.Snapshot(context.Background(), provider, now)
	if err != nil || snapshot.Day.Tokens != 0 || snapshot.Month.Tokens != 0 || snapshot.Total.Tokens != 4 ||
		snapshot.Total.Requests != 1 {
		t.Fatalf("restart, duplicate completion or original UTC periods lost: %+v %v", snapshot, err)
	}
}

func TestPartitionedUsageSnapshotFailsWholeOnCorruptOwnerLive(t *testing.T) {
	store, _ := usageFixture(t)
	provider, now := "corrupt-"+rand.Text(), time.Now().UTC()
	defer cleanupUsageKeys(t, store, provider, nil, now)
	if err := store.Set(context.Background(), usageKeys(provider, "", 173, now)[0], []byte("wrong-type")); err != nil {
		t.Fatal(err)
	}
	snapshot, err := NewAccounting(store).Snapshot(context.Background(), provider, now)
	if err == nil || snapshot != (domusage.Snapshot{}) {
		t.Fatalf("a failed owner became a partial successful report: %+v %v", snapshot, err)
	}
}
