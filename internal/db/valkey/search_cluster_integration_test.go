package valkey

import (
	"context"
	"crypto/rand"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/redis/rueidis"

	"github.com/kailas-cloud/vecdex/internal/db"
)

// Small transactions must become visible on remote shards without a local
// search acting as an ingestion barrier. Two commands also preserve MULTI/EXEC
// on replicas. Search 1.2.1 strands one mutation below writer-threads per node.
func TestClusterSmallTransactionSearchVisibilityLive(t *testing.T) {
	addresses := os.Getenv("VECDEX_E2E_VALKEY_CLUSTER_ADDRS")
	if testing.Short() || addresses == "" {
		t.Skip("isolated Search cluster required")
	}
	store, err := NewStore(Config{Addrs: strings.Split(addresses, ",")})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	primaries := clusterPrimaryClients(t, ctx, store)
	if len(primaries) < 3 {
		t.Fatal("at least three primary nodes required")
	}
	index := "vecdex-pending-" + rand.Text()
	prefix := index + ":indexed:"
	if err := store.CreateIndex(ctx, &db.IndexDefinition{
		Name: index, Prefixes: []string{prefix},
		Fields: []db.IndexField{{Name: "status", Type: db.IndexFieldTag}},
	}); err != nil {
		t.Fatal(err)
	}
	keys := make([]string, 0, 2*len(primaries))
	defer func() {
		cleanup, done := context.WithTimeout(context.Background(), 5*time.Second)
		defer done()
		if err := store.DropIndex(cleanup, index); err != nil {
			t.Error(err)
		}
		for _, key := range keys {
			if err := store.Del(cleanup, key); err != nil {
				t.Error(err)
			}
		}
	}()
	if err := store.WaitForIndex(ctx, index); err != nil {
		t.Fatal(err)
	}
	for address, node := range primaries {
		key := localClusterTestKey(t, ctx, node, prefix)
		marker := strings.Replace(key, ":indexed:", ":marker:", 1)
		keys = append(keys, key, marker)
		client, release := node.Dedicate()
		_, committed, err := execTransaction(ctx, client, []rueidis.Completed{
			client.B().Hset().Key(key).FieldValue().FieldValue("status", "run").Build(),
			client.B().Hset().Key(marker).FieldValue().FieldValue("commit", "1").Build(),
		})
		release()
		if err != nil || !committed {
			t.Fatalf("transaction on %s: committed=%t err=%v", address, committed, err)
		}
	}
	// Allow ordinary asynchronous ingestion to finish, without an FT.SEARCH on
	// any participant. FT.INFO does not drain the backend's private multi queue.
	time.Sleep(time.Second)
	assertSmallTransactionIndexParticipants(t, ctx, store, index)
	assertClusterFanoutCount(t, ctx, primaries, index)
}

func assertSmallTransactionIndexParticipants(t *testing.T, ctx context.Context, store *Store, index string) {
	t.Helper()
	participants, err := store.searchClients(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for address, node := range participants {
		fields, err := node.Do(ctx, node.B().Arbitrary("FT.INFO").Args(index, "LOCAL").Build()).AsMap()
		if err != nil {
			t.Fatal(err)
		}
		for _, field := range []string{"num_docs", "num_records"} {
			message, ok := fields[field]
			if !ok {
				t.Fatalf("participant %s omitted %s", address, field)
			}
			value, err := message.AsInt64()
			if err != nil || value != 1 {
				t.Fatalf("transaction on participant %s was not indexed without a local search: %s=%d err=%v",
					address, field, value, err)
			}
		}
	}
}

func assertClusterFanoutCount(t *testing.T, ctx context.Context, primaries map[string]rueidis.Client, index string) {
	t.Helper()
	var got int64
	for address, node := range primaries {
		command := node.B().Arbitrary("FT.SEARCH").Args(
			index, "@status:{run}", "ALLSHARDS", "CONSISTENT", "NOCONTENT", "LIMIT", "0", "100",
		).Build()
		raw, err := node.Do(ctx, command).ToArray()
		if err != nil || len(raw) == 0 {
			t.Fatalf("search from %s: %v", address, err)
		}
		got, err = raw[0].AsInt64()
		if err != nil || got != int64(len(primaries)) {
			t.Fatalf("successful fanout from %s returned %d documents, want %d; err=%v",
				address, got, len(primaries), err)
		}
	}
}

func clusterPrimaryClients(t *testing.T, ctx context.Context, store *Store) map[string]rueidis.Client {
	t.Helper()
	primaries := make(map[string]rueidis.Client)
	for address, node := range store.client.Nodes() {
		info, err := node.Do(ctx, node.B().Info().Section("replication").Build()).ToString()
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(info, "role:master") {
			primaries[address] = node
		}
	}
	return primaries
}

func localClusterTestKey(t *testing.T, ctx context.Context, node rueidis.Client, prefix string) string {
	t.Helper()
	for i := range 16384 {
		key := prefix + "{" + strconv.Itoa(i) + "}"
		// Direct node clients don't follow MOVED. No data is written by this probe.
		err := node.Do(ctx, node.B().Exists().Key(key).Build()).Error()
		if err == nil {
			return key
		}
		if !isRedisErr(err, "MOVED") {
			t.Fatal(fmt.Errorf("find local test slot: %w", err))
		}
	}
	t.Fatal("primary has no owned test slot")
	return ""
}

func TestClusterSearchCoordinatorDistributionLive(t *testing.T) {
	addresses := os.Getenv("VECDEX_E2E_VALKEY_CLUSTER_ADDRS")
	if testing.Short() || addresses == "" {
		t.Skip("isolated Search cluster with three balanced primaries required")
	}
	store, err := NewStore(Config{Addrs: strings.Split(addresses, ",")})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	primaries := clusterPrimaryClients(t, ctx, store)
	if len(primaries) != 3 {
		t.Fatal("fixture must have three balanced primaries")
	}
	index := "vecdex-query-routing-" + rand.Text()
	if err := store.CreateIndex(ctx, &db.IndexDefinition{
		Name: index, Prefixes: []string{index + ":data:"},
		Fields: []db.IndexField{{Name: "status", Type: db.IndexFieldTag}},
	}); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := store.DropIndex(ctx, index); err != nil {
			t.Error(err)
		}
	}()
	if err := store.WaitForIndex(ctx, index); err != nil {
		t.Fatal(err)
	}
	before := clusterSearchCallCounts(t, ctx, primaries)
	for range 96 {
		command := store.searchCommand([]string{index, "@status:{run}", "ALLSHARDS", "CONSISTENT", "LIMIT", "0", "10"})
		if _, err := store.client.Do(ctx, command).ToArray(); err != nil {
			t.Fatal(err)
		}
	}
	after := clusterSearchCallCounts(t, ctx, primaries)
	for address, calls := range after {
		delta := calls - before[address]
		t.Logf("primary coordinator %s served %d of 96 queries", address, delta)
		if delta < 16 || delta > 64 {
			t.Fatalf("one collection's coordinator load is skewed: %v -> %v", before, after)
		}
	}
}

func clusterSearchCallCounts(t *testing.T, ctx context.Context, nodes map[string]rueidis.Client) map[string]int64 {
	t.Helper()
	counts := make(map[string]int64, len(nodes))
	for address, node := range nodes {
		raw, err := node.Do(ctx, node.B().Info().Section("commandstats").Build()).ToString()
		if err != nil {
			t.Fatal(err)
		}
		stats := ""
		for name, value := range infoFields(raw) {
			if strings.EqualFold(name, "cmdstat_ft.search") {
				stats = value
				break
			}
		}
		if stats == "" {
			counts[address] = 0
			continue
		}
		calls, _, _ := strings.Cut(strings.TrimPrefix(stats, "calls="), ",")
		counts[address], err = strconv.ParseInt(calls, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
	}
	return counts
}
