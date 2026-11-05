package vecdex

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/kailas-cloud/vecdex/internal/domain/storage"
	chiTransport "github.com/kailas-cloud/vecdex/internal/transport/chi"
	gen "github.com/kailas-cloud/vecdex/internal/transport/generated"
	batchuc "github.com/kailas-cloud/vecdex/internal/usecase/batch"
	collectionuc "github.com/kailas-cloud/vecdex/internal/usecase/collection"
	documentuc "github.com/kailas-cloud/vecdex/internal/usecase/document"
	searchuc "github.com/kailas-cloud/vecdex/internal/usecase/search"
)

func TestIdempotencyIntegration(t *testing.T) {
	requireJSONMetadataFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	embedded, embedder := newMetadataIntegrationClient(t, ctx)
	defer embedded.Close()
	server := chiTransport.NewServer(embedded.collSvc.(*collectionuc.Service), embedded.docSvc.(*documentuc.Service),
		embedded.searchSvc.(*searchuc.Service), embedded.batchSvc.(*batchuc.Service), nil, nil, zap.NewNop())
	router := gen.Handler(server)
	var drop atomic.Bool
	drop.Store(true)
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut && r.Header.Get("Idempotency-Key") == "lost-response" && drop.Swap(false) {
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, r)
			if recorder.Code != http.StatusCreated {
				t.Errorf("fault injection did not follow a successful commit: %d %s", recorder.Code, recorder.Body)
			}
			connection, _, err := w.(http.Hijacker).Hijack()
			if err == nil {
				_ = connection.Close()
			}
			return
		}
		router.ServeHTTP(w, r)
	}))
	defer endpoint.Close()
	remote, err := NewHTTP(endpoint.URL, "", WithHTTPClient(endpoint.Client()))
	if err != nil {
		t.Fatal(err)
	}
	defer remote.Close()
	for label, client := range map[string]*Client{"embedded": embedded, "http": remote} {
		t.Run(label, func(t *testing.T) {
			name := fmt.Sprintf("requests_%s_%d", label, time.Now().UnixNano())
			if _, err := client.Collections().Ensure(ctx, name); err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := client.Collections().Delete(ctx, name); err != nil {
					t.Error(err)
				}
			}()
			assertIdempotentSourceLifecycle(t, ctx, client, embedder, name)
			assertConcurrentIdempotentWrites(t, ctx, client, embedder, name)
			assertIdempotentBatch(t, ctx, client, embedder, name)
			assertRequestIntentAndSourceScope(t, ctx, client, name)
			assertTypedRequestOptions(t, ctx, client, name)
			assertCompactRequestStorage(t, ctx, embedded, name)
			assertRequestCollectionGeneration(t, ctx, client, name)
			if label == "http" {
				assertLostHTTPWriteResponse(t, ctx, client, embedder, name)
			}
		})
	}
}

func assertTypedRequestOptions(t *testing.T, ctx context.Context, client *Client, name string) {
	t.Helper()
	type source struct {
		ID      string `vecdex:"id,id"`
		Content string `vecdex:"content,content"`
	}
	index, err := NewIndex[source](client, name)
	if err != nil {
		t.Fatal(err)
	}
	item := source{ID: "typed-request", Content: "SciFact typed source"}
	first, err := index.Upsert(ctx, item, IdempotencyKey("typed-create"))
	if err != nil {
		t.Fatal(err)
	}
	again, err := index.Upsert(ctx, item, IdempotencyKey("typed-create"))
	if err != nil || again != first {
		t.Fatalf("typed write did not preserve its request key: %+v %v", again, err)
	}
	if _, err := index.Upsert(ctx, item, IfVersion("stale")); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("typed write dropped its version condition: %v", err)
	}
	if err := index.Delete(ctx, item.ID, IfVersion("stale")); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("typed delete dropped its version condition: %v", err)
	}
	for range 2 {
		if err := index.Delete(ctx, item.ID, IfVersion(first.Version), IdempotencyKey("typed-delete")); err != nil {
			t.Fatalf("typed delete did not replay its committed result: %v", err)
		}
	}
}

func assertRequestCollectionGeneration(t *testing.T, ctx context.Context, client *Client, name string) {
	t.Helper()
	docs := client.Documents(name)
	source := Document{ID: "generation", Content: "SciFact original generation"}
	old, err := docs.Upsert(ctx, source, IdempotencyKey("generation-key"))
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Collections().Delete(ctx, name); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Collections().Ensure(ctx, name); err != nil {
		t.Fatal(err)
	}
	source.Content = "SciFact new collection generation"
	fresh, err := docs.Upsert(ctx, source, IdempotencyKey("generation-key"))
	if err != nil || !fresh.Created || fresh.Version == old.Version {
		t.Fatalf("request receipt crossed collection generations: %+v %v", fresh, err)
	}
	stored, err := docs.Get(ctx, source.ID)
	if err != nil || stored.Content != source.Content || stored.Version != fresh.Version {
		t.Fatalf("new generation did not retain its own source: %+v %v", stored, err)
	}
}

func assertRequestIntentAndSourceScope(t *testing.T, ctx context.Context, client *Client, name string) {
	t.Helper()
	docs := client.Documents(name)
	for _, id := range []string{"intent-a", "intent-b"} {
		result, err := docs.Upsert(ctx, Document{ID: id, Content: "SciFact source intent",
			Metadata: map[string]any{"keep": true}}, IdempotencyKey("same-key-different-source"))
		if err != nil || !result.Created || result.ID != id {
			t.Fatalf("idempotency key crossed source IDs: %+v %v", result, err)
		}
	}
	text := "SciFact new text"
	if _, err := docs.Patch(ctx, "intent-a", DocumentPatch{Content: &text}, IdempotencyKey("patch-intent")); err != nil {
		t.Fatal(err)
	}
	var clearMetadata map[string]any
	_, err := docs.Patch(ctx, "intent-a", DocumentPatch{Content: &text, Metadata: &clearMetadata},
		IdempotencyKey("patch-intent"))
	if !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("clearing metadata replayed a content-only patch: %v", err)
	}
	stored, err := docs.Get(ctx, "intent-a")
	if err != nil || stored.Metadata["keep"] != true {
		t.Fatalf("conflicting retry changed metadata: %+v %v", stored, err)
	}
	_, err = docs.Patch(ctx, "intent-a", DocumentPatch{Content: &text}, IfVersion("stale"),
		IdempotencyKey("failed-condition"))
	if !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("stale write condition was accepted: %v", err)
	}
	_, err = docs.Patch(ctx, "intent-a", DocumentPatch{Content: &text}, IfVersion(stored.Version),
		IdempotencyKey("failed-condition"))
	if err != nil {
		t.Fatalf("failed condition retained its request reservation: %v", err)
	}
}

func assertConcurrentIdempotentWrites(
	t *testing.T, ctx context.Context, client *Client, embedder *metadataEmbedder, name string,
) {
	t.Helper()
	docs := client.Documents(name)
	before := embedder.calls.Load()
	results := make([]WriteResult, 8)
	failures := make([]error, len(results))
	var group sync.WaitGroup
	for i := range results {
		group.Go(func() {
			results[i], failures[i] = docs.Upsert(ctx,
				Document{ID: "concurrent", Content: "SciFact concurrent request"}, IdempotencyKey("one-operation"))
		})
	}
	group.Wait()
	final, err := docs.Upsert(ctx, Document{ID: "concurrent", Content: "SciFact concurrent request"},
		IdempotencyKey("one-operation"))
	if err != nil || embedder.calls.Load() != before+1 {
		t.Fatalf("concurrent duplicate requests repeated embedding or lost their receipt: %+v %v", final, err)
	}
	for i, result := range results {
		if failures[i] == nil && result != final {
			t.Fatalf("concurrent request got a different commit: %+v want %+v", result, final)
		}
		if failures[i] != nil && !errors.Is(failures[i], ErrIdempotencyInProgress) {
			t.Fatalf("unexpected duplicate request result: %v", failures[i])
		}
	}
}

func assertIdempotentBatch(
	t *testing.T, ctx context.Context, client *Client, embedder *metadataEmbedder, name string,
) {
	t.Helper()
	docs := client.Documents(name)
	items := []BatchUpsertItem{
		{ID: "batch-a", Content: "SciFact batch first", IdempotencyKey: "batch-create-a"},
		{ID: "batch-b", Content: "SciFact batch second", IdempotencyKey: "batch-create-b"},
	}
	first, err := docs.BatchUpsert(ctx, items)
	if err != nil || first.Succeeded != 2 {
		t.Fatalf("first keyed batch: %+v %v", first, err)
	}
	before := embedder.calls.Load()
	again, err := docs.BatchUpsert(ctx, items)
	if err != nil || again.Succeeded != 2 || embedder.calls.Load() != before {
		t.Fatalf("batch retry repeated embedding or failed: %+v %v", again, err)
	}
	for i := range items {
		if again.Results[i].Version != first.Results[i].Version {
			t.Fatal("batch retry changed a source version")
		}
	}
	items[0].Content = "different payload"
	mixed, err := docs.BatchUpsert(ctx, items)
	if err != nil || mixed.Succeeded != 1 || mixed.Failed != 1 ||
		!errors.Is(mixed.Results[0].Err, ErrIdempotencyConflict) || embedder.calls.Load() != before {
		t.Fatalf("batch key conflict was not independent per source: %+v %v", mixed, err)
	}
	assertIdempotentBatchDeletion(t, ctx, docs, items, first.Results)
}
func assertIdempotentBatchDeletion(
	t *testing.T, ctx context.Context, docs *DocumentService, items []BatchUpsertItem, results []BatchResult,
) {
	t.Helper()
	selectors := []BatchDeleteItem{
		{ID: items[0].ID, IfVersion: results[0].Version, IdempotencyKey: "batch-delete-a"},
		{ID: items[1].ID, IfVersion: results[1].Version, IdempotencyKey: "batch-delete-b"},
	}
	for range 2 {
		deleted := docs.BatchDelete(ctx, selectors)
		if deleted.Succeeded != 2 || deleted.Failed != 0 {
			t.Fatalf("keyed batch DELETE did not replay committed outcomes: %+v", deleted)
		}
	}
}

func assertIdempotentSourceLifecycle(
	t *testing.T, ctx context.Context, client *Client, embedder *metadataEmbedder, name string,
) {
	t.Helper()
	docs := client.Documents(name)
	source := Document{ID: "source", Content: strings.Repeat("SciFact request source. ", 64),
		Metadata: map[string]any{"nested": map[string]any{"keep": true}}}
	first, err := docs.Upsert(ctx, source, IdempotencyKey("create"))
	if err != nil || first.Version == "" || !first.Created {
		t.Fatalf("first keyed source: %+v %v", first, err)
	}
	before := embedder.calls.Load()
	repeated, err := docs.Upsert(ctx, source, IdempotencyKey("create"))
	if err != nil || repeated != first || embedder.calls.Load() != before {
		t.Fatalf("repeated PUT changed outcome or repeated embedding: %+v %v", repeated, err)
	}
	changed := source
	changed.Content = "different source"
	if _, err := docs.Upsert(ctx, changed, IdempotencyKey("create")); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("different input reused an idempotency key: %v", err)
	}
	update := map[string]any{"nested": map[string]any{"new": true}}
	content := "SciFact patched request source"
	patch := DocumentPatch{Metadata: &update, Content: &content}
	patched, err := docs.Patch(ctx, source.ID, patch, IfVersion(first.Version), IdempotencyKey("patch"))
	if err != nil || patched.Version == first.Version || patched.Created {
		t.Fatalf("keyed PATCH: %+v %v", patched, err)
	}
	again, err := docs.Patch(ctx, source.ID, patch, IfVersion(first.Version), IdempotencyKey("patch"))
	if err != nil || again != patched || embedder.calls.Load() != before+1 {
		t.Fatalf("CAS retry lost original PATCH receipt: %+v %v", again, err)
	}
	assertIdempotentDeletionAndRecreation(t, ctx, docs, source, changed, first, patched)
}

func assertIdempotentDeletionAndRecreation(
	t *testing.T, ctx context.Context, docs *DocumentService, source, changed Document, first, patched WriteResult,
) {
	t.Helper()
	if err := docs.Delete(ctx, source.ID, IfVersion(patched.Version), IdempotencyKey("delete")); err != nil {
		t.Fatal(err)
	}
	if err := docs.Delete(ctx, source.ID, IfVersion(patched.Version), IdempotencyKey("delete")); err != nil {
		t.Fatalf("retry of committed DELETE failed: %v", err)
	}
	replacement, err := docs.Upsert(ctx, changed)
	if err != nil {
		t.Fatal(err)
	}
	if err := docs.Delete(ctx, source.ID, IfVersion(patched.Version), IdempotencyKey("delete")); err != nil {
		t.Fatal(err)
	}
	old, err := docs.Upsert(ctx, source, IdempotencyKey("create"))
	if err != nil || old != first {
		t.Fatalf("old success receipt changed after recreation: %+v %v", old, err)
	}
	stored, err := docs.Get(ctx, source.ID)
	if err != nil || stored.Version != replacement.Version || stored.Content != changed.Content {
		t.Fatalf("retry changed a newer source incarnation: %+v %v", stored, err)
	}
}

func assertCompactRequestStorage(t *testing.T, ctx context.Context, client *Client, name string) {
	t.Helper()
	col, err := client.collSvc.Get(ctx, name)
	if err != nil {
		t.Fatal(err)
	}
	layout, err := storage.NewCollection(name, col.Generation())
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"create", "patch", "delete"} {
		receipt, err := client.store.HGetAll(ctx, layout.Request("source", key))
		if err != nil || receipt["state"] != "complete" || len(receipt["result"]) > 512 ||
			strings.Contains(receipt["result"], "SciFact") || strings.Contains(receipt["result"], "nested") {
			t.Fatalf("request receipt retained source data or lost its outcome: %v %v", receipt, err)
		}
	}
}

func assertLostHTTPWriteResponse(
	t *testing.T, ctx context.Context, client *Client, embedder *metadataEmbedder, name string,
) {
	t.Helper()
	docs := client.Documents(name)
	source := Document{ID: "lost", Content: "SciFact lost response"}
	before := embedder.calls.Load()
	_, _ = docs.Upsert(ctx, source, IdempotencyKey("lost-response"))
	result, err := docs.Upsert(ctx, source, IdempotencyKey("lost-response"))
	if err != nil || !result.Created || result.Version == "" || embedder.calls.Load() != before+1 {
		t.Fatalf("retry after lost response repeated the write or embedding: %+v %v", result, err)
	}
	stored, err := docs.Get(ctx, source.ID)
	if err != nil || stored.Version != result.Version || stored.Content != source.Content {
		t.Fatalf("lost-response receipt differs from native commit: %+v %v", stored, err)
	}
}
