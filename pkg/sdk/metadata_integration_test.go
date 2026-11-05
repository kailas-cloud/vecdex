package vecdex

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kailas-cloud/vecdex/internal/domain/storage"
)

type metadataEmbedder struct{ calls atomic.Int64 }

func (e *metadataEmbedder) Embed(ctx context.Context, text string) (EmbeddingResult, error) {
	e.calls.Add(1)
	return integrationEmbedder{}.Embed(ctx, text)
}

func TestJSONMetadataIntegration(t *testing.T) {
	requireJSONMetadataFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	client, embedder := newMetadataIntegrationClient(t, ctx)
	defer client.Close()
	name := fmt.Sprintf("sdk_metadata_%d", time.Now().UnixNano())
	if _, err := client.Collections().Ensure(ctx, name,
		WithField("kind", FieldTag), WithField("weight", FieldNumeric)); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := client.Collections().Delete(ctx, name); err != nil {
			t.Error(err)
		}
	}()
	text := strings.Repeat("SciFact complete agent memory source. ", 64)
	value := map[string]any{
		"kind": "fact", "weight": json.Number("2.5"), "big": json.Number("9007199254740993"), "nothing": nil,
		"nested": map[string]any{"keep": true, "remove": "old", "payload": strings.Repeat("m", 8000)},
		"array":  []any{true, nil, json.Number("9007199254740993")},
	}
	if _, err := client.Documents(name).Upsert(ctx, Document{ID: "source", Content: text, Metadata: value}); err != nil {
		t.Fatal(err)
	}
	stored, err := client.Documents(name).Get(ctx, "source")
	if err != nil || stored.Content != text || stored.Version == "" {
		t.Fatalf("stored content/version: %+v err=%v", stored, err)
	}
	requireSameMetadata(t, stored.Metadata, value)
	page, err := client.Documents(name).List(ctx, "", 100)
	if err != nil || len(page.Documents) != 1 {
		t.Fatalf("list: %+v %v", page, err)
	}
	requireSameMetadata(t, page.Documents[0].Metadata, value)
	assertMetadataSearch(t, ctx, client, name, "fact", value)
	assertSourceMetadataStoredOnce(t, ctx, client, name)
	patchValue := map[string]any{"kind": "memo", "weight": nil,
		"nested": map[string]any{"remove": nil, "new": true}, "array": []any{"replacement"}}
	before := embedder.calls.Load()
	updated, err := client.Documents(name).Patch(ctx, "source",
		DocumentPatch{Metadata: &patchValue}, IfVersion(stored.Version))
	if err != nil || updated.Version == stored.Version {
		t.Fatalf("metadata patch: %+v %v", updated, err)
	}
	if embedder.calls.Load() != before {
		t.Fatal("metadata-only patch invoked the embedding provider")
	}
	value["kind"] = "memo"
	delete(value, "weight")
	delete(value["nested"].(map[string]any), "remove")
	value["nested"].(map[string]any)["new"] = true
	value["array"] = []any{"replacement"}
	fetched, fetchErr := client.Documents(name).Get(ctx, "source")
	if fetchErr != nil || fetched.Version != updated.Version || fetched.Content != text {
		t.Fatalf("patched source differs from receipt: %+v %v", fetched, fetchErr)
	}
	requireSameMetadata(t, fetched.Metadata, value)
	assertMetadataSearch(t, ctx, client, name, "memo", value)
	verifyMetadataBatchAndClear(t, ctx, client, embedder, name, updated.Version)
}

func requireSameMetadata(t *testing.T, got, want map[string]any) {
	t.Helper()
	actual, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	expected, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(actual, expected) {
		t.Fatalf("metadata differs: got %s want %s", actual, expected)
	}
}

func assertMetadataSearch(t *testing.T, ctx context.Context, client *Client, name, kind string, want map[string]any) {
	t.Helper()
	for _, mode := range []SearchMode{ModeKeyword, ModeSemantic, ModeHybrid} {
		deadline := time.Now().Add(5 * time.Second)
		for {
			response, err := client.Search(name).Query(ctx, "SciFact", &SearchOptions{
				Mode: mode, Limit: 10, Filters: FilterExpression{Must: []FilterCondition{{Key: "kind", Match: kind}}},
			})
			if err == nil && len(response.Results) == 1 && response.Results[0].ID == "source" {
				requireSameMetadata(t, response.Results[0].Metadata, want)
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s JSON metadata search: %+v %v", mode, response, err)
			}
			time.Sleep(25 * time.Millisecond)
		}
	}
}

func assertSourceMetadataStoredOnce(t *testing.T, ctx context.Context, client *Client, name string) {
	t.Helper()
	col, err := client.collSvc.Get(ctx, name)
	if err != nil {
		t.Fatal(err)
	}
	layout, err := storage.NewCollection(name, col.Generation())
	if err != nil {
		t.Fatal(err)
	}
	parent, err := client.store.HGetAll(ctx, layout.Document("source"))
	if err != nil || !strings.Contains(parent["__source_metadata"], "payload") {
		t.Fatalf("missing source metadata: %v", err)
	}
	var children []string
	if err := json.Unmarshal([]byte(parent["__children"]), &children); err != nil || len(children) < 2 {
		t.Fatalf("fragmented source expected: %d %v", len(children), err)
	}
	for _, key := range children {
		child, err := client.store.HGetAll(ctx, key)
		if err != nil || child["__source_metadata"] != "" || strings.Contains(child["__metadata"], "payload") {
			t.Fatalf("arbitrary metadata duplicated into fragment %s: %v", key, err)
		}
	}
}

func verifyMetadataBatchAndClear(
	t *testing.T, ctx context.Context, client *Client, embedder *metadataEmbedder, name, version string,
) {
	t.Helper()
	before := embedder.calls.Load()
	batch, err := client.Documents(name).BatchUpsert(ctx, []BatchUpsertItem{
		{ID: "valid", Content: "SciFact valid", Metadata: map[string]any{"kind": "batch", "free": []any{nil, true}}},
		{ID: "invalid", Content: "SciFact invalid", Metadata: map[string]any{"weight": "wrong numeric type"}},
	})
	if err != nil || batch.Succeeded != 1 || batch.Failed != 1 || embedder.calls.Load()-before != 1 {
		t.Fatalf("batch validation must precede embedding: %+v %v calls=%d", batch, err, embedder.calls.Load()-before)
	}
	stored, err := client.Documents(name).Get(ctx, "valid")
	if err != nil {
		t.Fatal(err)
	}
	requireSameMetadata(t, stored.Metadata, map[string]any{"kind": "batch", "free": []any{nil, true}})
	var emptyMetadata map[string]any
	cleared, err := client.Documents(name).Patch(ctx, "source",
		DocumentPatch{Metadata: &emptyMetadata}, IfVersion(version))
	fetched, fetchErr := client.Documents(name).Get(ctx, "source")
	if err != nil || fetchErr != nil || fetched.Version != cleared.Version ||
		len(fetched.Metadata) != 0 || cleared.Version == version {
		t.Fatalf("clear all metadata: %+v %v", cleared, err)
	}
}

func newMetadataIntegrationClient(t *testing.T, ctx context.Context) (*Client, *metadataEmbedder) {
	t.Helper()
	embedder := &metadataEmbedder{}
	opts := []Option{WithEmbedder(embedder), WithVectorDimensions(3)}
	if seeds := os.Getenv("VECDEX_E2E_VALKEY_CLUSTER_ADDRS"); seeds != "" {
		opts = append(opts, WithValkeyAddresses(strings.Split(seeds, ","), ""))
	} else {
		opts = append(opts, WithValkey(resolveIntegrationValkeyAddr(), ""), WithStandalone())
	}
	client, err := New(ctx, opts...)
	if err != nil {
		t.Fatal(err)
	}
	return client, embedder
}

func requireJSONMetadataFixture(t *testing.T) {
	t.Helper()
	if testing.Short() || (os.Getenv("VECDEX_E2E_VALKEY_ADDR") == "" &&
		os.Getenv("VECDEX_E2E_VALKEY_CLUSTER_ADDRS") == "") {
		t.Skip("isolated official Search fixture required")
	}
}
