package vecdex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"os"
	"os/exec"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kailas-cloud/vecdex/internal/db"
	"github.com/kailas-cloud/vecdex/internal/domain/storage"
)

// The container opt-in authorizes destructive topology changes to the isolated
// tests/cluster fixture. Never point it at an existing development cluster.
func TestClusterTopologyIntegration(t *testing.T) {
	container := os.Getenv("VECDEX_E2E_CLUSTER_CONTAINER")
	if testing.Short() || container == "" || os.Getenv("VECDEX_E2E_VALKEY_CLUSTER_ADDRS") == "" {
		t.Skip("disposable Search cluster with replicas required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	client, err := newIntegrationClient(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	name := fmt.Sprintf("sdk_topology_%d", time.Now().UnixNano())
	if _, err := client.Collections().Ensure(ctx, name, WithField("scope", FieldTag)); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanup, done := context.WithTimeout(context.Background(), 15*time.Second)
		defer done()
		if err := client.Collections().Delete(cleanup, name); err != nil {
			t.Error(err)
		}
	}()
	want := seedTopologyDocuments(t, ctx, client, name)
	index := topologyCollectionIndex(t, ctx, client, name)
	assertTopologySearch(t, ctx, client, name, want)
	workerCtx, stopWorker := context.WithCancel(ctx)
	workerDone := make(chan error, 1)
	var completed atomic.Int64
	go func() { workerDone <- exerciseTopologyTraffic(workerCtx, client, name, want, &completed) }()
	defer func() {
		stopWorker()
		if err := <-workerDone; err != nil {
			t.Error(err)
		}
	}()
	for _, phase := range []string{"expand", "contract", "failover"} {
		before := completed.Load()
		started := time.Now()
		arguments := []string{"exec", container, "/bin/sh", "/tests/topology.sh", phase}
		if phase == "expand" {
			arguments = append(arguments, index)
		}
		command := exec.CommandContext(ctx, "docker", arguments...)
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("%s: %v\n%s", phase, err, output)
		}
		assertTopologySearch(t, ctx, client, name, want)
		// Reconnect without losing committed collection metadata or source versions.
		restarted, err := newIntegrationClient(ctx, "")
		if err != nil {
			t.Fatal(err)
		}
		assertTopologySearch(t, ctx, restarted, name, want)
		restarted.Close()
		t.Logf("%s: %s, completed traffic cycles=%d",
			phase, time.Since(started).Round(time.Millisecond), completed.Load()-before)
	}
	if completed.Load() == 0 {
		t.Fatal("topology changed without any successful concurrent traffic cycle")
	}
}

func topologyCollectionIndex(t *testing.T, ctx context.Context, client *Client, name string) string {
	t.Helper()
	col, err := client.collSvc.Get(ctx, name)
	if err != nil {
		t.Fatal(err)
	}
	layout, err := storage.NewCollection(name, col.Generation())
	if err != nil {
		t.Fatal(err)
	}
	return layout.Index()
}

func topologySearch(ctx context.Context, client *Client, name string, mode SearchMode, want []string) error {
	response, err := client.Search(name).Query(ctx, "SciFact", &SearchOptions{
		Mode: mode, Limit: 100,
		Filters: FilterExpression{Must: []FilterCondition{{Key: "scope", Match: "stable"}}},
	})
	if err != nil {
		return err
	}
	got := make([]string, len(response.Results))
	for i, hit := range response.Results {
		got[i] = hit.ID
	}
	slices.Sort(got)
	if !slices.Equal(got, want) {
		missing := []string{}
		for _, id := range want {
			if !slices.Contains(got, id) {
				missing = append(missing, id)
			}
		}
		message := fmt.Sprintf(
			"successful %s search returned %d sources, want %d; missing=%v",
			mode,
			len(got),
			len(want),
			missing,
		)
		return &topologyInvariantError{message: message}
	}
	return nil
}

func assertTopologySearch(t *testing.T, ctx context.Context, client *Client, name string, want []string) {
	t.Helper()
	for _, mode := range []SearchMode{ModeKeyword, ModeSemantic, ModeHybrid} {
		deadline := time.Now().Add(15 * time.Second)
		for {
			if err := topologySearch(ctx, client, name, mode, want); err == nil {
				break
			} else if time.Now().After(deadline) {
				reportClusterSearchFailure(t, ctx, client, name, want)
				t.Fatal(err)
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
}

func exerciseTopologyTraffic(
	ctx context.Context, client *Client, name string, want []string, completed *atomic.Int64,
) error {
	for iteration := 0; ctx.Err() == nil; iteration++ {
		operation, cancel := context.WithTimeout(ctx, 3*time.Second)
		if ctx.Err() != nil {
			cancel()
			break
		}
		err := topologyTrafficCycle(operation, client, name, want, iteration)
		cancel()
		if err != nil {
			// Transport/topology failures may be retried, but a successful incomplete
			// search and a successful read of incorrect source content are violations.
			var invariant *topologyInvariantError
			if errors.As(err, &invariant) {
				return err
			}
			time.Sleep(50 * time.Millisecond)
			continue
		}
		completed.Add(1)
	}
	return nil
}

type topologyInvariantError struct{ message string }

func (e *topologyInvariantError) Error() string { return e.message }

func topologyTrafficCycle(ctx context.Context, client *Client, name string, want []string, iteration int) error {
	source := Document{
		ID: "changing", Content: fmt.Sprintf("temporary write %d", iteration),
		Metadata: map[string]any{"scope": "churn"},
	}
	if _, err := client.Documents(name).Upsert(ctx, source); err != nil {
		return err
	}
	got, err := client.Documents(name).Get(ctx, source.ID)
	if err != nil {
		return err
	}
	if got.Content != source.Content {
		return &topologyInvariantError{message: "successful read returned incorrect committed content"}
	}
	if err := client.Documents(name).Delete(ctx, source.ID); err != nil {
		return err
	}
	for _, mode := range []SearchMode{ModeKeyword, ModeSemantic, ModeHybrid} {
		if err := topologySearch(ctx, client, name, mode, want); err != nil {
			// Distinguish server errors from a successful but incomplete reply.
			var invariant *topologyInvariantError
			if errors.As(err, &invariant) {
				return err
			}
			return err
		}
	}
	return nil
}

func seedTopologyDocuments(t *testing.T, ctx context.Context, client *Client, name string) []string {
	t.Helper()
	docs := make([]BatchUpsertItem, 64)
	want := make([]string, len(docs))
	for i := range docs {
		want[i] = fmt.Sprintf("stable-%03d", i)
		docs[i] = BatchUpsertItem{
			ID: want[i], Content: "SciFact immutable source " + want[i],
			Metadata: map[string]any{"scope": "stable"},
		}
	}
	response, err := client.Documents(name).BatchUpsert(ctx, docs)
	if err != nil || response.Succeeded != len(docs) {
		t.Fatalf("batch=%+v err=%v", response, err)
	}

	return want
}

func reportClusterSearchFailure(t *testing.T, ctx context.Context, client *Client, name string, want []string) {
	t.Helper()
	col, err := client.collSvc.Get(ctx, name)
	if err != nil {
		t.Log(err)
		return
	}
	layout, _ := storage.NewCollection(name, col.Generation())
	filters, _ := toInternalFilters(FilterExpression{Must: []FilterCondition{{Key: "scope", Match: "stable"}}})
	raw, err := client.store.SearchBM25(
		ctx,
		&db.TextQuery{
			IndexName:    layout.Index(),
			Query:        "SciFact",
			Filters:      filters,
			TopK:         200,
			ReturnFields: []string{"__parent_id", "__version", "scope"},
		},
	)
	if err != nil {
		t.Log(err)
		return
	}
	ids := make([]string, 0, len(raw.Entries))
	for _, entry := range raw.Entries {
		id := entry.Fields["__parent_id"]
		ids = append(ids, id)
		parent, _ := client.store.HGetAll(ctx, layout.Document(id))
		if parent["__chunk_version"] != entry.Fields["__version"] {
			t.Logf(
				"version mismatch id=%s parent=%s child=%s",
				id,
				parent["__chunk_version"],
				entry.Fields["__version"],
			)
		}
	}
	t.Logf("native count=%d projected rows=%d", raw.Total, len(raw.Entries))
	for _, id := range want {
		if slices.Contains(ids, id) {
			continue
		}
		parent, _ := client.store.HGetAll(ctx, layout.Document(id))
		var keys []string
		_ = json.Unmarshal([]byte(parent["__children"]), &keys)
		t.Logf("missing id=%s parent version=%s children=%v", id, parent["__chunk_version"], keys)
		for _, key := range keys {
			fields, err := client.store.HGetAll(ctx, key)
			t.Logf("physical fragment id=%s version=%s err=%v", fields["__parent_id"], fields["__version"], err)
		}
	}
}
