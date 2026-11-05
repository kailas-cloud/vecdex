package vecdex

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"

	chiTransport "github.com/kailas-cloud/vecdex/internal/transport/chi"
	gen "github.com/kailas-cloud/vecdex/internal/transport/generated"
	collectionuc "github.com/kailas-cloud/vecdex/internal/usecase/collection"
	documentuc "github.com/kailas-cloud/vecdex/internal/usecase/document"
)

func TestHTTPOpaqueVersionIntegration(t *testing.T) {
	if testing.Short() ||
		(os.Getenv("VECDEX_E2E_VALKEY_CLUSTER_ADDRS") == "" && os.Getenv("VECDEX_E2E_VALKEY_ADDR") == "") {
		t.Skip("isolated Search fixture required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	client, err := newIntegrationClient(ctx, resolveIntegrationValkeyAddr())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	name := createIntegrationCollection(t, ctx, client)
	defer func() {
		if err := client.Collections().Delete(ctx, name); err != nil {
			t.Error(err)
		}
	}()
	server := chiTransport.NewServer(client.collSvc.(*collectionuc.Service), client.docSvc.(*documentuc.Service),
		nil, nil, nil, nil, zap.NewNop())
	endpoint := httptest.NewServer(gen.Handler(server))
	defer endpoint.Close()
	url := endpoint.URL + "/api/v2/collections/" + name + "/documents/source"
	transport := endpoint.Client()
	first := requireHTTPVersionReply(t, ctx, transport, http.MethodPut, url, "", "original", http.StatusCreated)
	if first.Version == "" || first.ETag != strconv.Quote(first.Version) {
		t.Fatal("HTTP creation omitted the opaque source validator")
	}
	winner := competeHTTPVersionWrites(t, ctx, transport, url, first.ETag)
	current := requireHTTPVersionReply(t, ctx, transport, http.MethodGet, url, "", "", http.StatusOK)
	if current.Version != winner.Version || current.Content != winner.RequestedContent || current.ETag == first.ETag {
		t.Fatal("HTTP conditional winner was not committed")
	}
	patched := requireHTTPVersionReply(t, ctx, transport, http.MethodPatch, url, winner.ETag, "patched", http.StatusOK)
	if patched.Version == winner.Version {
		t.Fatal("HTTP PATCH did not advance the source version")
	}
	assertHTTPVersionDeleteRecreate(t, ctx, transport, url, patched.ETag, &first)
}

func assertHTTPVersionDeleteRecreate(
	t *testing.T, ctx context.Context, transport *http.Client, url, deletedETag string, original *httpVersionReply,
) {
	t.Helper()
	requireHTTPVersionReply(t, ctx, transport, http.MethodDelete, url, deletedETag, "", http.StatusNoContent)
	missing := requireHTTPVersionReply(t, ctx, transport, http.MethodDelete, url, deletedETag, "", http.StatusConflict)
	if missing.ETag != "" || missing.CurrentVersion != "" {
		t.Fatal("deleted document received a current validator")
	}
	replacement := requireHTTPVersionReply(
		t, ctx, transport, http.MethodPut, url, "", "new incarnation", http.StatusCreated,
	)
	if replacement.Version == original.Version {
		t.Fatal("recreated source must change its opaque version")
	}
	assertHTTPVersionRecreation(t, ctx, transport, url, original.ETag, replacement.Version)
}

type httpVersionReply struct {
	Status           int    `json:"-"`
	RequestedContent string `json:"-"`
	ETag             string `json:"-"`
	Version          string `json:"version"`
	Content          string `json:"content"`
	Code             string `json:"code"`
	CurrentVersion   string `json:"current_version"`
}

func httpVersionRequest(
	ctx context.Context, client *http.Client, method, url, etag, content string,
) (httpVersionReply, error) {
	var reply httpVersionReply
	body, err := json.Marshal(map[string]string{"content": content})
	if err != nil {
		return reply, err
	}
	request, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(body))
	if err != nil {
		return reply, err
	}
	request.Header.Set("Content-Type", "application/json")
	if etag != "" {
		request.Header.Set("If-Match", etag)
	}
	response, err := client.Do(request)
	if err != nil {
		return reply, err
	}
	defer response.Body.Close()
	reply.Status, reply.ETag = response.StatusCode, response.Header.Get("ETag")
	reply.RequestedContent = content
	if response.StatusCode != http.StatusNoContent {
		err = json.NewDecoder(response.Body).Decode(&reply)
	}
	return reply, err
}

func requireHTTPVersionReply(
	t *testing.T, ctx context.Context, client *http.Client, method, url, etag, content string, status int,
) httpVersionReply {
	t.Helper()
	reply, err := httpVersionRequest(ctx, client, method, url, etag, content)
	if err != nil || reply.Status != status {
		t.Fatalf("%s: reply=%+v err=%v", method, reply, err)
	}
	return reply
}

func competeHTTPVersionWrites(
	t *testing.T, ctx context.Context, client *http.Client, url, etag string,
) httpVersionReply {
	t.Helper()
	replies := make([]httpVersionReply, 8)
	errors := make([]error, len(replies))
	var group sync.WaitGroup
	for i := range replies {
		group.Go(func() {
			replies[i], errors[i] = httpVersionRequest(ctx, client, http.MethodPut, url, etag, fmt.Sprintf("writer %d", i))
		})
	}
	group.Wait()
	var winner httpVersionReply
	winners := 0
	for i, reply := range replies {
		if errors[i] != nil {
			t.Fatal(errors[i])
		}
		if reply.Status == http.StatusOK {
			winner, winners = reply, winners+1
		} else if reply.Status != http.StatusConflict || reply.Code != "version_conflict" {
			t.Fatalf("unexpected conditional HTTP response: %+v", reply)
		}
	}
	if winners != 1 {
		t.Fatalf("conditional HTTP writes have %d winners, want one", winners)
	}
	return winner
}

func assertHTTPVersionRecreation(
	t *testing.T, ctx context.Context, client *http.Client, url, oldETag, currentVersion string,
) {
	t.Helper()
	for _, method := range []string{http.MethodPut, http.MethodPatch, http.MethodDelete} {
		reply := requireHTTPVersionReply(t, ctx, client, method, url, oldETag, "stale", http.StatusConflict)
		if reply.Code != "version_conflict" || reply.CurrentVersion != currentVersion ||
			reply.ETag != strconv.Quote(currentVersion) {
			t.Fatalf("stale HTTP mutation failed to return the recreated source validator: %+v", reply)
		}
	}
	current := requireHTTPVersionReply(t, ctx, client, http.MethodGet, url, "", "", http.StatusOK)
	if current.Content != "new incarnation" {
		t.Fatal("a stale HTTP validator changed the recreated document")
	}
}
