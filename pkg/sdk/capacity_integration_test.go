package vecdex

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/redis/rueidis"

	"github.com/kailas-cloud/vecdex/internal/domain/storage"
)

const (
	capacityDimensions = 1536
	capacityNodeLimit  = 128 * 1024 * 1024
	capacityMaxSources = 50000
	capacityBuckets    = 1024
)

type capacityEmbedder struct{}

func (capacityEmbedder) Embed(_ context.Context, text string) (EmbeddingResult, error) {
	vector := make([]float32, capacityDimensions)
	if strings.Contains(text, "SciFact") {
		vector[0] = 1
	} else {
		// Distinct dense vectors prevent interning identical one-hot payloads
		// from understating the memory retained by deleted HNSW records.
		digest := sha256.Sum256([]byte(text))
		generator := rand.New(rand.NewPCG(binary.LittleEndian.Uint64(digest[:8]),
			binary.LittleEndian.Uint64(digest[8:16])))
		var norm float64
		for i := 1; i < len(vector); i++ {
			vector[i] = float32(generator.Float64()*2 - 1)
			norm += float64(vector[i]) * float64(vector[i])
		}
		scale := float32(math.Sqrt(norm))
		for i := 1; i < len(vector); i++ {
			vector[i] /= scale
		}
	}
	return EmbeddingResult{Embedding: vector, PromptTokens: 1, TotalTokens: 1}, nil
}

func TestClusterMemoryCapacityIntegration(t *testing.T) {
	if os.Getenv("VECDEX_E2E_MEMORY_RECLAMATION") == "1" {
		t.Skip("run the online traffic and reclamation contracts on separate fresh fixtures")
	}
	runMemoryCapacityIntegration(t, true)
}

// This separate contract measures memory reclamation while queries are drained
// during topology changes. The online contract above remains a required gate.
func TestClusterMemoryReclamationIntegration(t *testing.T) {
	if os.Getenv("VECDEX_E2E_MEMORY_RECLAMATION") != "1" {
		t.Skip("explicit opt-in to the separate memory reclamation contract required")
	}
	runMemoryCapacityIntegration(t, false)
}

func runMemoryCapacityIntegration(t *testing.T, online bool) {
	t.Helper()
	container := os.Getenv("VECDEX_E2E_CLUSTER_CONTAINER")
	seeds := os.Getenv("VECDEX_E2E_VALKEY_CLUSTER_ADDRS")
	if testing.Short() || container == "" || seeds == "" || os.Getenv("VECDEX_E2E_MEMORY_PRESSURE") != "1" {
		t.Skip("explicit opt-in to the disposable memory-pressure fixture required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	runCapacityScript(t, ctx, container, "memory.sh", "limit", strconv.Itoa(capacityNodeLimit))
	client, trace := newTracedCapacityClient(t, ctx, seeds)
	defer client.Close()
	name := fmt.Sprintf("sdk_capacity_%d", time.Now().UnixNano())
	if _, err := client.Collections().Ensure(ctx, name,
		WithField("scope", FieldTag), WithField("bucket", FieldTag)); err != nil {
		t.Fatal(err)
	}
	col, err := client.collSvc.Get(ctx, name)
	if err != nil {
		t.Fatal(err)
	}
	layout, err := storage.NewCollection(name, col.Generation())
	if err != nil {
		t.Fatal(err)
	}
	want := seedTopologyDocuments(t, ctx, client, name)
	assertTopologySearch(t, ctx, client, name, want)
	if online {
		stopTraffic := startCapacitySearchTraffic(t, ctx, cancel, client, trace, name, want)
		defer stopTraffic()
	}
	acked, attempted := fillCapacityToOOM(t, ctx, client, name, seeds, layout.Index())
	before := snapshotCapacity(t, ctx, seeds, layout.Index(), "full")
	runCapacityScript(t, ctx, container, "topology.sh", "expand", layout.Index())
	if err := client.store.WaitForIndex(ctx, layout.Index()); err != nil {
		t.Fatal(err)
	}
	moved := snapshotCapacity(t, ctx, seeds, layout.Index(), "moved")
	verifyCapacityMovement(t, before, moved)
	primaries := capacityPrimaryPorts(before)
	newPrimaries := make(map[string]string)
	for _, port := range primaries {
		output := runCapacityScript(t, ctx, container, "memory.sh", "rebuild", port, layout.Index())
		newPrimaries[port] = capacityPromotedPort(t, output)
	}
	rebuilt := snapshotCapacity(t, ctx, seeds, layout.Index(), "rebuilt")
	verifyCapacityReclamation(t, moved, rebuilt, newPrimaries)
	assertTopologySearch(t, ctx, client, name, want)
	// Resumed writes must use the same limits after all replicas and primaries
	// have been rebuilt. A new client also checks metadata after role changes.
	restarted, err := New(ctx, WithValkeyAddresses(strings.Split(seeds, ","), ""),
		WithEmbedder(capacityEmbedder{}), WithVectorDimensions(capacityDimensions))
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	verifyCapacityInventory(t, ctx, restarted, name, acked, attempted)
	resumeCapacityWrites(t, ctx, restarted, name)
}

func fillCapacityToOOM(
	t *testing.T, ctx context.Context, client *Client, name, seeds, index string,
) (acked, attempted map[string]bool) {
	t.Helper()
	acked, attempted = make(map[string]bool), make(map[string]bool)
	ingestor := NewTextIngestor(client.Documents(name), &IngestorConfig{BatchSize: 16, Parallelism: 8})
	for offset := 0; offset < capacityMaxSources; offset += 256 {
		docs := make([]BatchUpsertItem, min(256, capacityMaxSources-offset))
		for i := range docs {
			source := capacityDocument(offset + i)
			docs[i] = BatchUpsertItem{ID: source.ID, Content: source.Content, Metadata: source.Metadata}
			attempted[docs[i].ID] = true
		}
		response, err := ingestor.Upsert(ctx, docs)
		if err != nil {
			t.Fatal(err)
		}
		full := false
		for _, item := range response.Results {
			switch {
			case item.OK:
				acked[item.ID] = true
			case item.Err != nil && strings.Contains(strings.ToUpper(item.Err.Error()), "OOM"):
				full = true
			default:
				t.Fatalf("unexpected fill error for %s: %v", item.ID, item.Err)
			}
		}
		if offset%2048 == 0 || full {
			t.Logf("capacity fill: acknowledged=%d attempted=%d OOM=%t", len(acked), len(attempted), full)
			snapshotCapacity(t, ctx, seeds, index, "fill")
		}
		if full {
			return acked, attempted
		}
	}
	t.Fatal("bounded corpus did not reach the configured memory limit")
	return nil, nil
}

func capacityDocument(number int) Document {
	id := fmt.Sprintf("bulk-%06d", number)
	return Document{ID: id, Content: "capacity source " + id,
		Metadata: map[string]any{"scope": "bulk", "bucket": strconv.Itoa(number % capacityBuckets)}}
}

type capacityNode struct {
	Port           string `json:"port"`
	Role           string `json:"role"`
	Used           int64  `json:"used"`
	RSS            int64  `json:"rss"`
	Limit          int64  `json:"limit"`
	VectorCapacity int64  `json:"vector_capacity"`
	VectorSize     int64  `json:"vector_size"`
	VectorMemory   int64  `json:"vector_memory"`
	SearchMemory   int64  `json:"search_memory"`
	Reclaimable    int64  `json:"reclaimable"`
}

func snapshotCapacity(
	t *testing.T, ctx context.Context, seeds, index, phase string,
) map[string]capacityNode {
	t.Helper()
	admin, err := rueidis.NewClient(rueidis.ClientOption{
		InitAddress: strings.Split(seeds, ","), DisableCache: true, AlwaysRESP2: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	result := make(map[string]capacityNode)
	for address, node := range admin.Nodes() {
		_, port, ok := strings.Cut(address, ":")
		if !ok {
			t.Fatalf("invalid fixture address %s", address)
		}
		info, err := node.Do(ctx, node.B().Info().Section("memory", "replication", "modules").Build()).ToString()
		if err != nil {
			t.Fatal(err)
		}
		fields := capacityInfoFields(info)
		sample := capacityNode{Port: port, Role: fields["role"], Used: capacityInt(t, fields["used_memory"]),
			RSS: capacityInt(t, fields["used_memory_rss"]), Limit: capacityInt(t, fields["maxmemory"])}
		sample.SearchMemory = capacityInt(t, fields["search_used_memory_bytes"])
		sample.Reclaimable = capacityInt(t, fields["search_index_reclaimable_memory"])
		indexInfo, err := node.Do(ctx, node.B().Arbitrary("FT.INFO").Args(index, "LOCAL").Build()).AsMap()
		if err != nil {
			t.Fatal(err)
		}
		attributeMessage := indexInfo["attributes"]
		attributes, err := attributeMessage.ToArray()
		if err != nil {
			t.Fatal(err)
		}
		for _, attribute := range attributes {
			value, err := attribute.AsMap()
			if err != nil {
				t.Fatal(err)
			}
			alias := value["attribute"]
			if text, _ := alias.ToString(); text != "vector" {
				continue
			}
			memory := value["user_indexed_memory"]
			sample.VectorMemory, err = memory.AsInt64()
			if err != nil {
				t.Fatal(err)
			}
			vector := value["index"]
			stats, err := vector.AsMap()
			if err != nil {
				t.Fatal(err)
			}
			capacity, size := stats["capacity"], stats["size"]
			sample.VectorCapacity, _ = capacity.AsInt64()
			sample.VectorSize, _ = size.AsInt64()
		}
		result[port] = sample
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("memory %s: %s", phase, encoded)
	return result
}

func capacityInfoFields(info string) map[string]string {
	fields := make(map[string]string)
	for line := range strings.SplitSeq(info, "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), ":")
		if ok {
			fields[key] = value
		}
	}
	return fields
}

func capacityInt(t *testing.T, value string) int64 {
	t.Helper()
	result, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func capacityPrimaryPorts(nodes map[string]capacityNode) []string {
	var ports []string
	for port, node := range nodes {
		if node.Role == "master" {
			ports = append(ports, port)
		}
	}
	slices.Sort(ports)
	return ports
}

func verifyCapacityMovement(t *testing.T, before, moved map[string]capacityNode) {
	t.Helper()
	if len(capacityPrimaryPorts(before)) != 3 || len(capacityPrimaryPorts(moved)) != 4 {
		t.Fatal("memory fixture did not grow from three to four primaries")
	}
	for port, sample := range before {
		if sample.Role != "master" {
			continue
		}
		if moved[port].VectorSize >= sample.VectorSize {
			t.Fatalf("primary %s did not release any indexed source keys", port)
		}
	}
	if moved["17395"].VectorSize == 0 {
		t.Fatal("new primary has no transferred index records")
	}
}

func verifyCapacityReclamation(
	t *testing.T, moved, rebuilt map[string]capacityNode, replacements map[string]string,
) {
	t.Helper()
	for oldPort, newPort := range replacements {
		before, after := moved[oldPort], rebuilt[newPort]
		if after.Role != "master" || after.Limit != capacityNodeLimit || after.Used >= after.Limit {
			t.Fatalf("rebuilt primary did not regain capacity: %+v", after)
		}
		if after.VectorSize != before.VectorSize || after.VectorMemory != before.VectorMemory {
			t.Fatalf("external rebuild changed live index contents: %+v -> %+v", before, after)
		}
		// HNSW capacity is a graph reservation, not allocated vector bytes.
		// Deleted vectors remain in graph routing even below that reservation.
		if before.Reclaimable == 0 || after.Reclaimable != 0 ||
			after.SearchMemory >= before.SearchMemory || after.Used >= before.Used || after.RSS >= before.RSS {
			t.Fatalf("external rebuild did not reclaim deleted graph memory: %+v -> %+v", before, after)
		}
	}
	for _, node := range rebuilt {
		if node.Limit != capacityNodeLimit || node.Used >= node.Limit {
			t.Fatalf("a participant still exceeds the unchanged memory limit: %+v", node)
		}
	}
}

func runCapacityScript(t *testing.T, ctx context.Context, container, script string, arguments ...string) string {
	t.Helper()
	args := append([]string{"exec", container, "/bin/sh", "/tests/" + script}, arguments...)
	output, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v: %v\n%s", script, arguments, err, output)
	}
	return string(output)
}

func capacityPromotedPort(t *testing.T, output string) string {
	t.Helper()
	for line := range strings.SplitSeq(output, "\n") {
		if port, ok := strings.CutPrefix(strings.TrimSpace(line), "NEW_PRIMARY="); ok {
			return port
		}
	}
	t.Fatal("external rebuild omitted the new primary address")
	return ""
}

func verifyCapacityInventory(
	t *testing.T, ctx context.Context, client *Client, name string, acked, attempted map[string]bool,
) {
	t.Helper()
	got := capacityCatalog(t, ctx, client, name)
	for id := range acked {
		if doc, ok := got[id]; !ok || doc.Content != "capacity source "+id {
			t.Fatalf("acknowledged source %s was lost or changed", id)
		}
	}
	verifyCapacityIndex(t, ctx, client, name, got, attempted)
	t.Logf("verified capacity catalog and index: %d sources, %d acknowledged bulk sources", len(got), len(acked))
}

func capacityCatalog(t *testing.T, ctx context.Context, client *Client, name string) map[string]Document {
	t.Helper()
	got := make(map[string]Document)
	cursor := ""
	for {
		page, err := client.Documents(name).List(ctx, cursor, 100)
		if err != nil {
			t.Fatal(err)
		}
		for _, doc := range page.Documents {
			if doc.Version == "" {
				t.Fatalf("source %s lost its version", doc.ID)
			}
			got[doc.ID] = doc
		}
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
	}
	return got
}

func verifyCapacityIndex(
	t *testing.T, ctx context.Context, client *Client, name string, got map[string]Document, attempted map[string]bool,
) {
	t.Helper()
	buckets := make(map[string][]string)
	for id, doc := range got {
		if doc.Metadata["scope"] != "bulk" {
			continue
		}
		if !attempted[id] {
			t.Fatalf("unexpected source in capacity catalog: %s", id)
		}
		bucket, ok := doc.Metadata["bucket"].(string)
		if !ok {
			t.Fatalf("capacity source has no bucket: %s", id)
		}
		buckets[bucket] = append(buckets[bucket], id)
	}
	for bucket, want := range buckets {
		response, err := client.Search(name).Query(ctx, "capacity", &SearchOptions{Mode: ModeKeyword, Limit: 100,
			Filters: FilterExpression{Must: []FilterCondition{{Key: "scope", Match: "bulk"}, {Key: "bucket", Match: bucket}}}})
		if err != nil {
			t.Fatal(err)
		}
		ids := make([]string, len(response.Results))
		for i, hit := range response.Results {
			ids[i] = hit.ID
		}
		slices.Sort(ids)
		slices.Sort(want)
		if !slices.Equal(ids, want) {
			t.Fatalf("index bucket %s lost committed sources: got=%d want=%d", bucket, len(ids), len(want))
		}
	}
}

func resumeCapacityWrites(t *testing.T, ctx context.Context, client *Client, name string) {
	t.Helper()
	for i := range 256 {
		doc := capacityDocument(capacityMaxSources + i)
		if _, err := client.Documents(name).Upsert(ctx, doc); err != nil {
			t.Fatalf("write did not resume after scale-out and index rebuild: %v", err)
		}
		stored, err := client.Documents(name).Get(ctx, doc.ID)
		if err != nil || stored.Content != doc.Content {
			t.Fatalf("resumed write was not readable: %s %v", doc.ID, err)
		}
	}
	t.Log("resumed writes under unchanged memory limits: 256")
}

// A read-only worker continues searching even when OOM prevents writes. It must
// observe exact immutable IDs through migration, full sync and index backfill.
func startCapacitySearchTraffic(
	t *testing.T, ctx context.Context, abort context.CancelFunc,
	client *Client, trace *capacityTraceStore, name string, want []string,
) func() {
	t.Helper()
	workerCtx, stop := context.WithCancel(ctx)
	done := make(chan error, 1)
	var completed atomic.Int64
	go func() {
		for workerCtx.Err() == nil {
			for _, mode := range []SearchMode{ModeKeyword, ModeSemantic, ModeHybrid} {
				operation, cancel := context.WithTimeout(workerCtx, 2*time.Second)
				err := topologySearch(operation, client, name, mode, want)
				cancel()
				var invariant *topologyInvariantError
				if errors.As(err, &invariant) {
					trace.report(t)
					done <- err
					abort()
					return
				}
				if err == nil {
					completed.Add(1)
				}
			}
			select {
			case <-workerCtx.Done():
			case <-time.After(50 * time.Millisecond):
			}
		}
		done <- nil
	}()
	return func() {
		stop()
		if err := <-done; err != nil {
			t.Error(err)
		}
		if completed.Load() == 0 {
			t.Error("no successful concurrent search during the memory-pressure contract")
		}
		t.Logf("complete concurrent searches during the memory-pressure contract: %d", completed.Load())
	}
}
