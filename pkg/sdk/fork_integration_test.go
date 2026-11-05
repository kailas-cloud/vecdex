package vecdex

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/redis/rueidis"
)

type forkNodeEvidence struct {
	Address       string            `json:"address"`
	Role          string            `json:"role"`
	Configuration map[string]string `json:"search_configuration"`
	BeforeKeys    []string          `json:"before_local_stable_keys"`
	AfterKeys     []string          `json:"after_local_stable_keys"`
	SavesBefore   int64             `json:"saves_before"`
	SavesAfter    int64             `json:"saves_after"`
	ActiveQueries int               `json:"queries_bracketed_by_active_snapshot"`
	ElapsedMillis int64             `json:"elapsed_millis"`
}

type forkWindowEvidence struct {
	Container     string             `json:"container"`
	Collection    string             `json:"collection"`
	BulkSources   int                `json:"bulk_sources"`
	PayloadBytes  int                `json:"payload_bytes_per_source"`
	Nodes         []forkNodeEvidence `json:"nodes"`
	TrafficCycles int64              `json:"completed_sdk_traffic_cycles"`
	StableSources int                `json:"verified_stable_sources"`
	Complete      bool               `json:"complete"`
}

// Explicitly opts into BGSAVE on every primary and replica of the disposable
// fixture. LOCAL queries exercise the Search fork callback; the SDK worker also
// checks normal distributed search and source writes throughout the snapshots.
func TestClusterForkWindowIntegration(t *testing.T) {
	container, seeds := os.Getenv("VECDEX_E2E_CLUSTER_CONTAINER"), os.Getenv("VECDEX_E2E_VALKEY_CLUSTER_ADDRS")
	if testing.Short() || container == "" || seeds == "" {
		t.Skip("disposable three-primary Search fixture with replicas required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	client, err := newIntegrationClient(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	name := fmt.Sprintf("sdk_fork_%d", time.Now().UnixNano())
	evidence := forkWindowEvidence{Container: container, Collection: name, BulkSources: 512, PayloadBytes: 48 * 1024}
	defer saveForkWindowEvidence(t, &evidence)
	if _, err := client.Collections().Ensure(ctx, name, WithField("scope", FieldTag)); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanup, done := context.WithTimeout(context.Background(), time.Minute)
		defer done()
		if err := client.Collections().Delete(cleanup, name); err != nil {
			t.Error(err)
		}
	}()
	want := seedTopologyDocuments(t, ctx, client, name)
	versions := make(map[string]string, len(want))
	verifyRecoveredSources(t, ctx, client, name, want, versions)
	index := topologyCollectionIndex(t, ctx, client, name)
	seedForkPayload(t, ctx, client, name, evidence.BulkSources, evidence.PayloadBytes)
	if err := client.store.WaitForIndex(ctx, index); err != nil {
		t.Fatal(err)
	}
	assertTopologySearch(t, ctx, client, name, want)
	admin, nodes := forkFixtureNodes(t, seeds)
	defer admin.Close()
	workerCtx, stopWorker := context.WithCancel(ctx)
	workerDone := make(chan error, 1)
	var completed atomic.Int64
	go func() { workerDone <- exerciseTopologyTraffic(workerCtx, client, name, want, &completed) }()
	var stopOnce sync.Once
	finishTraffic := func() {
		stopOnce.Do(func() {
			stopWorker()
			if err := <-workerDone; err != nil {
				t.Error(err)
			}
			evidence.TrafficCycles = completed.Load()
		})
	}
	defer finishTraffic()
	addresses := make([]string, 0, len(nodes))
	for address := range nodes {
		addresses = append(addresses, address)
	}
	slices.Sort(addresses)
	roles := make(map[string]int)
	for _, address := range addresses {
		sample := forkNodeEvidence{Address: address}
		evidence.Nodes = append(evidence.Nodes, sample)
		proveNodeForkWindow(t, ctx, nodes[address], index, &evidence.Nodes[len(evidence.Nodes)-1])
		roles[evidence.Nodes[len(evidence.Nodes)-1].Role]++
	}
	finishTraffic()
	if evidence.TrafficCycles == 0 || roles["master"] != 3 || roles["slave"] != 3 {
		t.Fatalf("missing concurrent traffic or primary/replica coverage: cycles=%d roles=%v", evidence.TrafficCycles, roles)
	}
	verifyReconnectedTopology(t, ctx, client, name, want, index, versions)
	evidence.StableSources, evidence.Complete = len(want), !t.Failed()
}

func forkFixtureNodes(t *testing.T, seeds string) (admin rueidis.Client, nodes map[string]rueidis.Client) {
	t.Helper()
	var err error
	admin, err = rueidis.NewClient(rueidis.ClientOption{InitAddress: strings.Split(seeds, ","),
		DisableCache: true, AlwaysRESP2: true})
	if err != nil {
		t.Fatal(err)
	}
	nodes = admin.Nodes()
	if len(nodes) != 6 {
		admin.Close()
		t.Fatalf("fresh six-member fixture required, got %d nodes", len(nodes))
	}
	return admin, nodes
}

func seedForkPayload(t *testing.T, ctx context.Context, client *Client, name string, count, size int) {
	t.Helper()
	docs := make([]BatchUpsertItem, count)
	for i := range docs {
		// Different random payloads keep the RDB child active without changing
		// server timing settings or using a compressible repeated string.
		payload := make([]byte, size/2)
		if _, err := rand.Read(payload); err != nil {
			t.Fatal(err)
		}
		docs[i] = BatchUpsertItem{ID: fmt.Sprintf("bulk-%04d", i), Content: "snapshot payload",
			Metadata: map[string]any{"scope": "bulk", "payload": hex.EncodeToString(payload)}}
	}
	ingestor := NewTextIngestor(client.Documents(name), &IngestorConfig{BatchSize: 16, Parallelism: 8})
	response, err := ingestor.Upsert(ctx, docs)
	if err != nil || response.Succeeded != count {
		t.Fatalf("fork corpus: succeeded=%d want=%d err=%v", response.Succeeded, count, err)
	}
}

func proveNodeForkWindow(
	t *testing.T, ctx context.Context, node rueidis.Client, index string, sample *forkNodeEvidence,
) {
	t.Helper()
	started := time.Now()
	defer func() { sample.ElapsedMillis = time.Since(started).Milliseconds() }()
	info := forkPersistenceInfo(t, ctx, node)
	sample.Role = info["role"]
	sample.SavesBefore = capacityInt(t, info["rdb_saves"])
	sample.Configuration = forkSearchConfiguration(t, ctx, node)
	sample.BeforeKeys = forkLocalStableKeys(t, ctx, node, index)
	if len(sample.BeforeKeys) == 0 {
		t.Fatalf("no stable indexed records on %s", sample.Address)
	}
	for attempt := 0; attempt < 3 && sample.ActiveQueries == 0; attempt++ {
		observeForkSnapshot(t, ctx, node, index, sample)
	}
	sample.SavesAfter = capacityInt(t, forkPersistenceInfo(t, ctx, node)["rdb_saves"])
	sample.AfterKeys = forkLocalStableKeys(t, ctx, node, index)
	if sample.ActiveQueries == 0 || !slices.Equal(sample.BeforeKeys, sample.AfterKeys) {
		t.Fatalf("fork window not proved on %s: active queries=%d", sample.Address, sample.ActiveQueries)
	}
	t.Logf("%s %s: %d queries inside BGSAVE, saves=%d→%d", sample.Address, sample.Role,
		sample.ActiveQueries, sample.SavesBefore, sample.SavesAfter)
}

func forkSearchConfiguration(t *testing.T, ctx context.Context, node rueidis.Client) map[string]string {
	t.Helper()
	configuration, err := node.Do(ctx, node.B().Arbitrary("CONFIG", "GET").Args(
		"search.writer-threads", "search.max-worker-suspension-secs").Build()).AsMap()
	if err != nil {
		t.Fatal(err)
	}
	result := make(map[string]string, len(configuration))
	for key, value := range configuration {
		result[key], err = value.ToString()
		if err != nil {
			t.Fatal(err)
		}
	}
	if result["search.writer-threads"] != "1" || result["search.max-worker-suspension-secs"] != "0" {
		t.Fatalf("unsupported fixture configuration: %v", result)
	}
	return result
}

func observeForkSnapshot(
	t *testing.T, ctx context.Context, node rueidis.Client, index string, sample *forkNodeEvidence,
) {
	t.Helper()
	before := forkPersistenceInfo(t, ctx, node)
	if before["rdb_bgsave_in_progress"] != "0" {
		t.Fatal("unexpected pre-existing snapshot")
	}
	if err := node.Do(ctx, node.B().Arbitrary("BGSAVE").Build()).Error(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		beforeQuery := forkPersistenceInfo(t, ctx, node)
		if beforeQuery["rdb_bgsave_in_progress"] == "1" {
			keys := forkLocalStableKeys(t, ctx, node, index)
			afterQuery := forkPersistenceInfo(t, ctx, node)
			if !slices.Equal(keys, sample.BeforeKeys) {
				t.Fatalf("local stable keys changed during snapshot on %s", sample.Address)
			}
			if afterQuery["rdb_bgsave_in_progress"] == "1" {
				sample.ActiveQueries++
			}
		}
		finished := beforeQuery["rdb_bgsave_in_progress"] == "0"
		if finished && capacityInt(t, beforeQuery["rdb_saves"]) > capacityInt(t, before["rdb_saves"]) {
			if beforeQuery["rdb_last_bgsave_status"] != "ok" {
				t.Fatalf("snapshot failed: %v", beforeQuery)
			}
			return
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			t.Fatalf("snapshot did not complete on %s: %v", sample.Address, beforeQuery)
		}
	}
}

func forkPersistenceInfo(t *testing.T, ctx context.Context, node rueidis.Client) map[string]string {
	t.Helper()
	operation, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	info, err := node.Do(operation, node.B().Info().Section("persistence", "replication").Build()).ToString()
	if err != nil {
		t.Fatal(err)
	}
	return capacityInfoFields(info)
}

func forkLocalStableKeys(t *testing.T, ctx context.Context, node rueidis.Client, index string) []string {
	t.Helper()
	operation, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	values, err := node.Do(operation, node.B().Arbitrary("FT.SEARCH").Args(index,
		"@scope:{stable}", "LOCALONLY", "NOCONTENT", "LIMIT", "0", "1000").Build()).ToArray()
	if err != nil || len(values) == 0 {
		t.Fatalf("local Search during fork: %v", err)
	}
	total, err := values[0].AsInt64()
	if err != nil || total != int64(len(values)-1) {
		t.Fatalf("incomplete native local search: total=%d rows=%d err=%v", total, len(values)-1, err)
	}
	keys := make([]string, len(values)-1)
	for i, value := range values[1:] {
		keys[i], err = value.ToString()
		if err != nil {
			t.Fatal(err)
		}
	}
	slices.Sort(keys)
	return keys
}

func saveForkWindowEvidence(t *testing.T, evidence *forkWindowEvidence) {
	t.Helper()
	evidence.Complete = evidence.Complete && !t.Failed()
	encoded, err := json.MarshalIndent(evidence, "", "  ")
	if err != nil {
		t.Error(err)
		return
	}
	path := filepath.Join("..", "..", "tests", "reports")
	if err := os.MkdirAll(path, 0o750); err != nil {
		t.Error(err)
		return
	}
	if err := os.WriteFile(filepath.Join(path, "fork-window.json"), append(encoded, '\n'), 0o600); err != nil {
		t.Error(err)
	}
}
