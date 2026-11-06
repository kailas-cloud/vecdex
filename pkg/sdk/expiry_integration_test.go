package vecdex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kailas-cloud/vecdex/internal/db"
	"github.com/kailas-cloud/vecdex/internal/domain/storage"
)

type expiryEmbedder struct{ calls atomic.Int64 }

func (e *expiryEmbedder) Embed(_ context.Context, text string) (EmbeddingResult, error) {
	e.calls.Add(1)
	if len(text) > 512 {
		return EmbeddingResult{}, ErrEmbeddingInputTooLong
	}
	vector := []float32{1, 0, 0}
	if strings.Contains(text, "live far") {
		vector[0] = -1
	}
	return EmbeddingResult{Embedding: vector}, nil
}

func newExpiryClient(t *testing.T, ctx context.Context) (*Client, *expiryEmbedder) {
	t.Helper()
	embedder := &expiryEmbedder{}
	options := []Option{WithEmbedder(embedder), WithVectorDimensions(3)}
	if seeds := os.Getenv("VECDEX_E2E_VALKEY_CLUSTER_ADDRS"); seeds != "" {
		options = append(options, WithValkeyAddresses(strings.Split(seeds, ","), ""))
	} else {
		options = append(options, WithValkey(resolveIntegrationValkeyAddr(), ""), WithStandalone())
	}
	client, err := New(ctx, options...)
	if err != nil {
		t.Fatal(err)
	}
	return client, embedder
}

func TestDocumentExpiryIntegration(t *testing.T) {
	requireJSONMetadataFixture(t)
	for _, facade := range []string{"embedded"} {
		t.Run(facade, func(t *testing.T) { runDocumentExpiryContract(t, facade) })
	}
}

func runDocumentExpiryContract(t *testing.T, facade string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	embedded, embedder := newExpiryClient(t, ctx)
	defer embedded.Close()
	embedded.stopMaintenance() // Logical visibility must work with physical cleanup stopped.
	client := embedded
	name := fmt.Sprintf("expiry_%s_%d", facade, time.Now().UnixNano())
	if _, err := client.Collections().Ensure(ctx, name); err != nil {
		t.Fatal(err)
	}
	cleanupClient := embedded
	defer func() {
		cleanup, done := context.WithTimeout(context.Background(), 20*time.Second)
		defer done()
		if err := cleanupClient.Collections().Delete(cleanup, name); err != nil {
			t.Error(err)
		}
		cleanupClient.Close()
	}()
	col, err := embedded.collSvc.Get(ctx, name)
	if err != nil {
		t.Fatal(err)
	}
	layout, _ := storage.NewCollection(name, col.Generation())
	keys := prepareExpirySources(t, ctx, client, embedded, embedder, name, layout)
	assertExpiredSourcesInvisible(t, ctx, client, embedded, name, layout)
	assertExpiredSourceRecreation(t, ctx, client, embedded, name, layout)
	keys = slices.DeleteFunc(keys, func(key string) bool { return key == layout.Document("due-000") })
	embedded.Close()
	restarted, _ := newExpiryClient(t, ctx)
	cleanupClient = restarted
	waitForExpiryCleanup(t, ctx, restarted, layout, keys)
	count, err := restarted.Documents(name).Count(ctx)
	if err != nil || count != 3 {
		t.Fatalf("restart lost live sources: count=%d %v", count, err)
	}
}

func assertExpiredSourceRecreation(
	t *testing.T, ctx context.Context, client, embedded *Client, name string, layout storage.Collection,
) {
	t.Helper()
	row, err := embedded.store.HGetAll(ctx, layout.Document("due-000"))
	if err != nil || row["__source_version"] == "" {
		t.Fatalf("expired physical source was lost before recreation: %v %v", row, err)
	}
	docs := client.Documents(name)
	source := Document{ID: "due-000", Content: "SciFact marker live far replacement"}
	if _, err := docs.Upsert(ctx, source, IfVersion(row["__source_version"])); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("expired source accepted its previous version: %v", err)
	}
	fresh, err := docs.Upsert(ctx, source)
	if err != nil || !fresh.Created || fresh.Version == row["__source_version"] {
		t.Fatalf("expired source did not start a new incarnation: %+v %v", fresh, err)
	}
}

func prepareExpirySources(
	t *testing.T, ctx context.Context, client, embedded *Client, embedder *expiryEmbedder,
	name string, layout storage.Collection,
) []string {
	t.Helper()
	docs := client.Documents(name)
	if _, err := docs.Upsert(ctx, Document{ID: "live", Content: "SciFact marker live far"}); err != nil {
		t.Fatal(err)
	}
	assertDeadlinePatchDoesNotEmbed(t, ctx, docs, embedder)
	deadline := time.Now().Add(time.Second).UnixMilli()
	source := Document{ID: "long", Content: strings.Repeat("SciFact marker source. ", 100), ExpiresAt: &deadline}
	written, err := docs.Upsert(ctx, source, IdempotencyKey("long-create"))
	if err != nil {
		t.Fatal(err)
	}
	stored, err := docs.Get(ctx, source.ID)
	if err != nil || stored.Content != source.Content || stored.ExpiresAt == nil || *stored.ExpiresAt != deadline {
		t.Fatalf("deadline or whole source lost: %+v %v", stored, err)
	}
	past := time.Now().Add(-time.Second).UnixMilli()
	items := make([]BatchUpsertItem, 120) // Exceed the semantic candidate window with closer expired vectors.
	parentKeys := make([]string, 1, 1+len(items))
	parentKeys[0] = layout.Document("long")
	for i := range items {
		id := fmt.Sprintf("due-%03d", i)
		items[i] = BatchUpsertItem{ID: id, Content: "SciFact marker source", ExpiresAt: &past}
		parentKeys = append(parentKeys, layout.Document(id))
	}
	for offset := 0; offset < len(items); offset += 100 {
		batchItems := items[offset:min(offset+100, len(items))]
		batch, err := docs.BatchUpsert(ctx, batchItems)
		if err != nil || batch.Succeeded != len(batchItems) {
			t.Fatalf("expiring batch: %+v %v", batch, err)
		}
	}
	keys := expiryPayloadKeys(t, ctx, embedded, parentKeys)
	if wait := time.Until(time.UnixMilli(deadline)); wait > 0 {
		time.Sleep(wait)
	}
	before := embedder.calls.Load()
	replay, err := docs.Upsert(ctx, source, IdempotencyKey("long-create"))
	if err != nil || replay != written || embedder.calls.Load() != before {
		t.Fatalf("retry resurrected an expired source or changed its receipt: %+v %v", replay, err)
	}
	return keys
}

func assertDeadlinePatchDoesNotEmbed(
	t *testing.T, ctx context.Context, docs *DocumentService, embedder *expiryEmbedder,
) {
	t.Helper()
	deadline := time.Now().Add(time.Hour).UnixMilli()
	first, err := docs.Upsert(ctx, Document{ID: "scheduled", Content: "SciFact marker live far", ExpiresAt: &deadline})
	if err != nil {
		t.Fatal(err)
	}
	before := embedder.calls.Load()
	extended := deadline + time.Hour.Milliseconds()
	patched, err := docs.Patch(ctx, "scheduled", DocumentPatch{ExpiresAt: &extended},
		IfVersion(first.Version), IdempotencyKey("extend"))
	if err != nil {
		t.Fatal(err)
	}
	replay, err := docs.Patch(ctx, "scheduled", DocumentPatch{ExpiresAt: &extended},
		IfVersion(first.Version), IdempotencyKey("extend"))
	if err != nil || replay != patched {
		t.Fatalf("deadline-only CAS retry failed: %+v %v", replay, err)
	}
	persistent := int64(0)
	if _, err := docs.Patch(ctx, "scheduled", DocumentPatch{ExpiresAt: &persistent},
		IfVersion(patched.Version)); err != nil {
		t.Fatal(err)
	}
	stored, err := docs.Get(ctx, "scheduled")
	if err != nil || stored.ExpiresAt != nil || embedder.calls.Load() != before {
		t.Fatalf("clearing a deadline embedded or retained expiry: %+v %v", stored, err)
	}
}

func expiryPayloadKeys(t *testing.T, ctx context.Context, client *Client, parents []string) []string {
	t.Helper()
	rows, err := client.store.HGetAllMulti(ctx, parents)
	if err != nil || len(rows) != len(parents) {
		t.Fatalf("read expiry manifests: %v", err)
	}
	keys := append([]string(nil), parents...)
	for _, row := range rows {
		var children []string
		if err := json.Unmarshal([]byte(row["__children"]), &children); err != nil {
			t.Fatal(err)
		}
		keys = append(keys, children...)
	}
	return keys
}

func assertExpiredSourcesInvisible(
	t *testing.T, ctx context.Context, client, embedded *Client, name string, layout storage.Collection,
) {
	t.Helper()
	docs := client.Documents(name)
	if _, err := docs.Get(ctx, "long"); !errors.Is(err, ErrDocumentNotFound) {
		t.Fatalf("expired source is visible: %v", err)
	}
	if exists, err := embedded.store.Exists(ctx, layout.Document("long")); err != nil || !exists {
		t.Fatalf("the cleanup worker was not actually stopped: %v %v", exists, err)
	}
	if count, err := docs.Count(ctx); err != nil || count != 2 {
		t.Fatalf("expired sources remain in Count: %d %v", count, err)
	}
	page, err := docs.List(ctx, "", 100)
	if err != nil || len(page.Documents) != 0 || page.NextCursor == "" {
		t.Fatalf("expired ID window did not advance pagination: %+v %v", page, err)
	}
	assertLiveSearchWithExpiredCandidates(t, ctx, client, name)
}

func assertLiveSearchWithExpiredCandidates(t *testing.T, ctx context.Context, client *Client, name string) {
	t.Helper()
	for _, mode := range []SearchMode{ModeSemantic, ModeKeyword, ModeHybrid} {
		found, err := client.Search(name).Query(ctx, "marker", &SearchOptions{Mode: mode, Limit: 1})
		if err != nil || len(found.Results) != 1 ||
			(found.Results[0].ID != "live" && found.Results[0].ID != "scheduled") {
			t.Fatalf("%s did not prefilter expired candidates: %+v %v", mode, found, err)
		}
	}
}

func waitForExpiryCleanup(t *testing.T, ctx context.Context, client *Client, layout storage.Collection, keys []string) {
	t.Helper()
	deadline := time.Now().Add(40 * time.Second)
	for {
		rows, err := client.store.HGetAllMulti(ctx, keys)
		if err != nil {
			t.Fatal(err)
		}
		remaining := 0
		for _, row := range rows {
			remaining += len(row)
		}
		found, err := client.store.SearchBM25(ctx, &db.TextQuery{IndexName: layout.Index(), Query: "marker", TopK: 200})
		if err == nil && remaining == 0 && found.Total == 3 {
			break // This raw query has no expiry filter: physical index entries are gone too.
		}
		if time.Now().After(deadline) {
			t.Fatalf("restart did not clean expired payload/index: fields=%d search=%+v %v", remaining, found, err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
