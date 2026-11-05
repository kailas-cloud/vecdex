package vecdex

import (
	"context"
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"

	chiTransport "github.com/kailas-cloud/vecdex/internal/transport/chi"
	gen "github.com/kailas-cloud/vecdex/internal/transport/generated"
	batchuc "github.com/kailas-cloud/vecdex/internal/usecase/batch"
	collectionuc "github.com/kailas-cloud/vecdex/internal/usecase/collection"
	documentuc "github.com/kailas-cloud/vecdex/internal/usecase/document"
	searchuc "github.com/kailas-cloud/vecdex/internal/usecase/search"
)

func TestBatchVersionIntegration(t *testing.T) {
	requireJSONMetadataFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	embedded, _ := newMetadataIntegrationClient(t, ctx)
	defer embedded.Close()
	server := chiTransport.NewServer(embedded.collSvc.(*collectionuc.Service), embedded.docSvc.(*documentuc.Service),
		embedded.searchSvc.(*searchuc.Service), embedded.batchSvc.(*batchuc.Service), nil, nil, zap.NewNop())
	endpoint := httptest.NewServer(gen.Handler(server))
	defer endpoint.Close()
	remote, err := NewHTTP(endpoint.URL, "", WithHTTPClient(endpoint.Client()))
	if err != nil {
		t.Fatal(err)
	}
	defer remote.Close()
	for label, client := range map[string]*Client{"embedded": embedded, "http": remote} {
		t.Run(label, func(t *testing.T) {
			name := fmt.Sprintf("batch_version_%s_%d", label, time.Now().UnixNano())
			if _, err := client.Collections().Ensure(ctx, name); err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := client.Collections().Delete(ctx, name); err != nil {
					t.Error(err)
				}
			}()
			assertMixedBatchVersions(t, ctx, client, name)
			assertConcurrentBatchVersions(t, ctx, client, name)
		})
	}
}

func assertMixedBatchVersions(t *testing.T, ctx context.Context, client *Client, name string) {
	t.Helper()
	docs := client.Documents(name)
	original := make(map[string]Document)
	for _, id := range []string{"match", "stale", "recreated", "delete"} {
		original[id] = writeBatchVersionSource(t, ctx, docs, id, "original source")
	}
	current := writeBatchVersionSource(t, ctx, docs, "stale", "concurrent replacement")
	if err := docs.Delete(ctx, "recreated", IfVersion(original["recreated"].Version)); err != nil {
		t.Fatal(err)
	}
	recreated := writeBatchVersionSource(t, ctx, docs, "recreated", "new incarnation")
	long := strings.Repeat("SciFact committed batch source. ", 64)
	response, err := docs.BatchUpsert(ctx, []BatchUpsertItem{
		{ID: "match", Content: long, IfVersion: original["match"].Version},
		{ID: "stale", Content: "forbiddenbatchwrite", IfVersion: original["stale"].Version},
		{ID: "recreated", Content: "forbiddenbatchwrite", IfVersion: original["recreated"].Version},
		{ID: "new", Content: "new source"},
		{ID: "absent", Content: "forbiddenbatchwrite", IfVersion: "absent-version"},
	})
	if err != nil || response.Succeeded != 2 || response.Failed != 3 || len(response.Results) != 5 {
		t.Fatalf("mixed batch CAS: %+v %v", response, err)
	}
	assertBatchVersionConflict(t, response.Results[1], current.Version)
	assertBatchVersionConflict(t, response.Results[2], recreated.Version)
	assertBatchVersionConflict(t, response.Results[4], "")
	assertWrittenBatchVersion(t, ctx, docs, response.Results[0], long)
	assertWrittenBatchVersion(t, ctx, docs, response.Results[3], "new source")
	assertBatchVersionSource(t, ctx, docs, "stale", current.Version, current.Content)
	assertBatchVersionSource(t, ctx, docs, "recreated", recreated.Version, recreated.Content)
	if _, err := docs.Get(ctx, "absent"); !errors.Is(err, ErrDocumentNotFound) {
		t.Fatalf("failed conditional upsert created an absent source: %v", err)
	}
	deleted := docs.BatchDelete(ctx, []BatchDeleteItem{
		{ID: "match", IfVersion: original["match"].Version},
		{ID: "new", IfVersion: response.Results[3].Version},
		{ID: "recreated", IfVersion: original["recreated"].Version},
		{ID: "absent", IfVersion: "absent-version"},
		{ID: "delete", IfVersion: original["delete"].Version},
	})
	if deleted.Succeeded != 2 || deleted.Failed != 3 || len(deleted.Results) != 5 {
		t.Fatalf("mixed delete CAS: %+v", deleted)
	}
	assertBatchVersionConflict(t, deleted.Results[0], response.Results[0].Version)
	assertBatchVersionConflict(t, deleted.Results[2], recreated.Version)
	assertBatchVersionConflict(t, deleted.Results[3], "")
	assertBatchVersionSource(t, ctx, docs, "match", response.Results[0].Version, long)
	for _, id := range []string{"new", "delete"} {
		if _, err := docs.Get(ctx, id); !errors.Is(err, ErrDocumentNotFound) {
			t.Fatalf("successful batch delete left source %s: %v", id, err)
		}
	}
	search, err := client.Search(name).Query(ctx, "forbiddenbatchwrite", &SearchOptions{Mode: ModeKeyword})
	if err != nil || len(search.Results) != 0 {
		t.Fatalf("a failed CAS source became visible through Search: %+v %v", search, err)
	}
}

func writeBatchVersionSource(
	t *testing.T, ctx context.Context, docs *DocumentService, id, content string,
) Document {
	t.Helper()
	if _, err := docs.Upsert(ctx, Document{ID: id, Content: content}); err != nil {
		t.Fatal(err)
	}
	stored, err := docs.Get(ctx, id)
	if err != nil || stored.Version == "" {
		t.Fatalf("source %s has no version: %+v %v", id, stored, err)
	}
	return stored
}

func assertBatchVersionConflict(t *testing.T, item BatchResult, current string) {
	t.Helper()
	var conflict *VersionConflictError
	if item.OK || item.Version != "" || !errors.Is(item.Err, ErrRevisionConflict) ||
		!errors.As(item.Err, &conflict) || conflict.CurrentVersion != current {
		t.Fatalf("batch item lost its CAS conflict or current validator: %+v", item)
	}
}

func assertWrittenBatchVersion(
	t *testing.T, ctx context.Context, docs *DocumentService, item BatchResult, text string,
) {
	t.Helper()
	if !item.OK || item.Err != nil || item.Version == "" {
		t.Fatalf("successful upsert omitted its source version: %+v", item)
	}
	assertBatchVersionSource(t, ctx, docs, item.ID, item.Version, text)
}

func assertBatchVersionSource(t *testing.T, ctx context.Context, docs *DocumentService, id, version, text string) {
	t.Helper()
	stored, err := docs.Get(ctx, id)
	if err != nil || stored.Version != version || stored.Content != text {
		t.Fatalf("batch source %s differs from its committed result: %+v %v", id, stored, err)
	}
}

func assertConcurrentBatchVersions(t *testing.T, ctx context.Context, client *Client, name string) {
	t.Helper()
	docs := client.Documents(name)
	a := writeBatchVersionSource(t, ctx, docs, "race-a", "original a")
	b := writeBatchVersionSource(t, ctx, docs, "race-b", "original b")
	responses := make([]BatchResponse, 8)
	failures := make([]error, len(responses))
	var group sync.WaitGroup
	for i := range responses {
		group.Go(func() {
			responses[i], failures[i] = docs.BatchUpsert(ctx, []BatchUpsertItem{
				{ID: a.ID, Content: fmt.Sprintf("winner-%d-a", i), IfVersion: a.Version},
				{ID: b.ID, Content: fmt.Sprintf("winner-%d-b", i), IfVersion: b.Version},
			})
		})
	}
	group.Wait()
	winners := map[string]int{}
	for i, response := range responses {
		if failures[i] != nil || len(response.Results) != 2 {
			t.Fatalf("concurrent batch %d: %+v %v", i, response, failures[i])
		}
		for j, item := range response.Results {
			if item.OK {
				winners[item.ID]++
				assertWrittenBatchVersion(t, ctx, docs, item, fmt.Sprintf("winner-%d-%c", i, 'a'+j))
			} else if !errors.Is(item.Err, ErrRevisionConflict) {
				t.Fatalf("unexpected concurrent batch failure: %+v", item)
			}
		}
	}
	if winners[a.ID] != 1 || winners[b.ID] != 1 {
		t.Fatalf("batch CAS has multiple or missing winners per source: %v", winners)
	}
}
