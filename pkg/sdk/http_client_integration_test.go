package vecdex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"

	chiTransport "github.com/kailas-cloud/vecdex/internal/transport/chi"
	gen "github.com/kailas-cloud/vecdex/internal/transport/generated"
	batchuc "github.com/kailas-cloud/vecdex/internal/usecase/batch"
	collectionuc "github.com/kailas-cloud/vecdex/internal/usecase/collection"
	documentuc "github.com/kailas-cloud/vecdex/internal/usecase/document"
	healthuc "github.com/kailas-cloud/vecdex/internal/usecase/health"
	searchuc "github.com/kailas-cloud/vecdex/internal/usecase/search"
	usageuc "github.com/kailas-cloud/vecdex/internal/usecase/usage"
)

func TestHTTPClientContractIntegration(t *testing.T) {
	requireJSONMetadataFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	embedded, embedder := newMetadataIntegrationClient(t, ctx)
	defer embedded.Close()
	server := chiTransport.NewServer(embedded.collSvc.(*collectionuc.Service), embedded.docSvc.(*documentuc.Service),
		embedded.searchSvc.(*searchuc.Service), embedded.batchSvc.(*batchuc.Service),
		embedded.usageSvc.(*usageuc.Service), embedded.healthSvc.(*healthuc.Service), zap.NewNop())
	endpoint := httptest.NewServer(gen.Handler(server))
	defer endpoint.Close()
	remote, err := NewHTTP(endpoint.URL, "", WithHTTPClient(endpoint.Client()))
	if err != nil {
		t.Fatal(err)
	}
	defer remote.Close()
	name := fmt.Sprintf("http_sdk_%d", time.Now().UnixNano())
	_, err = remote.Collections().Ensure(ctx, name, WithField("kind", FieldTag), WithField("weight", FieldNumeric))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := remote.Collections().Delete(ctx, name); err != nil {
			t.Error(err)
		}
	}()
	if _, err := remote.Collections().Ensure(ctx, name); err != nil {
		t.Fatal(err)
	}
	content := strings.Repeat("SciFact complete memory source. ", 64)
	metadata := map[string]any{"kind": "fact", "weight": 16777217.25,
		"big": json.Number("9007199254740993"), "nested": map[string]any{"keep": true, "remove": 1}}
	created, err := remote.Documents(name).Upsert(ctx, Document{ID: "source", Content: content, Metadata: metadata})
	if err != nil || !created.Created {
		t.Fatalf("remote upsert: created=%v err=%v", created, err)
	}
	stored, err := remote.Documents(name).Get(ctx, "source")
	if err != nil || stored.Content != content || stored.Version == "" {
		t.Fatalf("remote source: %+v %v", stored, err)
	}
	requireSameMetadata(t, stored.Metadata, metadata)
	before := embedder.calls.Load()
	update := map[string]any{"kind": "memo", "nested": map[string]any{"remove": nil}}
	updated, err := remote.Documents(name).
		Patch(ctx, "source", DocumentPatch{Metadata: &update}, IfVersion(stored.Version))
	if err != nil || updated.Version == stored.Version || embedder.calls.Load() != before {
		t.Fatalf("remote metadata CAS invoked embedding or lost source/version: %+v %v", updated, err)
	}
	assertHTTPClientConflict(t, ctx, remote, name, stored.Version, updated.Version)
	assertHTTPClientSearch(t, ctx, remote, name, updated.Version)
	assertHTTPClientBatchAndList(t, ctx, remote, name, updated.Version)
	assertHTTPClientStatus(t, ctx, remote)
}

func assertHTTPClientConflict(t *testing.T, ctx context.Context, client *Client, name, stale, current string) {
	t.Helper()
	err := client.Documents(name).Delete(ctx, "source", IfVersion(stale))
	var conflict *VersionConflictError
	var failure *HTTPError
	if !errors.Is(err, ErrRevisionConflict) || !errors.As(err, &conflict) || conflict.CurrentVersion != current ||
		!errors.As(err, &failure) || failure.StatusCode != 409 {
		t.Fatalf("remote conflict lost typed error or validator: %v", err)
	}
}

func assertHTTPClientSearch(t *testing.T, ctx context.Context, client *Client, name, version string) {
	t.Helper()
	weight := 16777217.25
	for _, mode := range []SearchMode{ModeKeyword, ModeSemantic, ModeHybrid} {
		response, err := client.Search(name).Query(ctx, "SciFact", &SearchOptions{Mode: mode, Limit: 5, MaxContentBytes: 20,
			Filters: FilterExpression{Must: []FilterCondition{
				{Key: "kind", Match: "memo"}, {Key: "weight", Range: &RangeFilter{GTE: &weight, LTE: &weight}},
			}}})
		if err != nil || len(response.Results) != 1 || response.Results[0].Version != version ||
			len(response.Results[0].Snippet) > 20 || response.Results[0].Metadata["big"] != json.Number("9007199254740993") {
			t.Fatalf("remote %s search: %+v %v", mode, response, err)
		}
	}
	type memory struct {
		ID      string `vecdex:"id,id"`
		Content string `vecdex:"text,content"`
		Kind    string `vecdex:"kind,tag"`
	}
	index, err := NewIndex[memory](client, name)
	if err != nil {
		t.Fatal(err)
	}
	hits, err := index.Search().Query("SciFact").Mode(ModeKeyword).Limit(5).MaxContentBytes(20).Do(ctx)
	if err != nil || len(hits) != 1 || hits[0].Item.Content != "" || hits[0].Item.Kind != "memo" ||
		hits[0].Version != version || hits[0].Snippet == "" {
		t.Fatalf("remote typed snippet/source contract: %+v %v", hits, err)
	}
}

func assertHTTPClientBatchAndList(t *testing.T, ctx context.Context, client *Client, name, version string) {
	t.Helper()
	batch, err := client.Documents(name).BatchUpsert(ctx, []BatchUpsertItem{
		{ID: "valid", Content: "batch memory", Metadata: map[string]any{"kind": "fact"}},
		{ID: "invalid", Content: "invalid indexed metadata", Metadata: map[string]any{"weight": "wrong type"}},
	})
	if err != nil || batch.Succeeded != 1 || batch.Failed != 1 || !errors.Is(batch.Results[1].Err, ErrInvalidSchema) {
		t.Fatalf("remote per-item batch errors: %+v %v", batch, err)
	}
	count, err := client.Documents(name).Count(ctx)
	if err != nil || count != 2 {
		t.Fatalf("remote source count: %d %v", count, err)
	}
	first, err := client.Documents(name).List(ctx, "", 1)
	if err != nil || len(first.Documents) != 1 || first.NextCursor == "" {
		t.Fatalf("remote source page: %+v %v", first, err)
	}
	second, err := client.Documents(name).List(ctx, first.NextCursor, 1)
	if err != nil || len(second.Documents) != 1 || first.Documents[0].ID == second.Documents[0].ID {
		t.Fatalf("remote next source page: %+v %v", second, err)
	}
	assertHTTPClientDeletion(t, ctx, client, name, version)
}

func assertHTTPClientDeletion(t *testing.T, ctx context.Context, client *Client, name, version string) {
	t.Helper()
	batch := client.Documents(name).BatchDelete(ctx, []BatchDeleteItem{{ID: "valid"}})
	if batch.Failed != 0 || batch.Succeeded != 1 {
		t.Fatalf("remote batch delete: %+v", batch)
	}
	if err := client.Documents(name).Delete(ctx, "source", IfVersion(version)); err != nil {
		t.Fatal(err)
	}
	_, err := client.Documents(name).Get(ctx, "source")
	if !errors.Is(err, ErrDocumentNotFound) {
		t.Fatalf("remote missing source error: %v", err)
	}
}

func assertHTTPClientStatus(t *testing.T, ctx context.Context, client *Client) {
	t.Helper()
	if err := client.Ping(ctx); err != nil {
		t.Fatal(err)
	}
	if status := client.Health(ctx); status.Status != "ok" {
		t.Fatalf("remote health: %+v", status)
	}
	if _, err := client.Usage(ctx, PeriodTotal); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Collections().List(ctx, "", 100); err != nil {
		t.Fatal(err)
	}
}
