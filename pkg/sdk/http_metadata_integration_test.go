package vecdex

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"go.uber.org/zap"

	chiTransport "github.com/kailas-cloud/vecdex/internal/transport/chi"
	gen "github.com/kailas-cloud/vecdex/internal/transport/generated"
	batchuc "github.com/kailas-cloud/vecdex/internal/usecase/batch"
	collectionuc "github.com/kailas-cloud/vecdex/internal/usecase/collection"
	documentuc "github.com/kailas-cloud/vecdex/internal/usecase/document"
	searchuc "github.com/kailas-cloud/vecdex/internal/usecase/search"
)

func TestHTTPJSONMetadataIntegration(t *testing.T) {
	requireJSONMetadataFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	client, err := newIntegrationClient(ctx, resolveIntegrationValkeyAddr())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	name := fmt.Sprintf("http_metadata_%d", time.Now().UnixNano())
	if _, err := client.Collections().Ensure(ctx, name, WithField("kind", FieldTag)); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := client.Collections().Delete(ctx, name); err != nil {
			t.Error(err)
		}
	}()
	server := chiTransport.NewServer(client.collSvc.(*collectionuc.Service), client.docSvc.(*documentuc.Service),
		client.searchSvc.(*searchuc.Service), client.batchSvc.(*batchuc.Service), nil, nil, zap.NewNop())
	endpoint := httptest.NewServer(gen.Handler(server))
	defer endpoint.Close()
	url := endpoint.URL + "/api/v2/collections/" + name + "/documents/source"
	transport := endpoint.Client()
	created := metadataHTTPRequest(t, ctx, transport, http.MethodPut, url, "",
		`{"content":"SciFact source","metadata":{"kind":"fact","big":9007199254740993,"nested":{"keep":true,"remove":1}}}`,
		http.StatusCreated)
	assertCompactHTTPWriteResult(t, &created)
	createdSource := metadataHTTPRequest(t, ctx, transport, http.MethodGet, url, "", "", http.StatusOK)
	assertHTTPMetadataNumber(t, createdSource.Metadata)
	retained := metadataHTTPRequest(t, ctx, transport, http.MethodPatch, url, created.ETag,
		`{"content":"SciFact revised text"}`, http.StatusOK)
	retainedSource := metadataHTTPRequest(t, ctx, transport, http.MethodGet, url, "", "", http.StatusOK)
	assertHTTPMetadataNumber(t, retainedSource.Metadata)
	patched := metadataHTTPRequest(t, ctx, transport, http.MethodPatch, url, retained.ETag,
		`{"metadata":{"nested":{"remove":null,"new":[true,null]}}}`, http.StatusOK)
	patchedSource := metadataHTTPRequest(t, ctx, transport, http.MethodGet, url, "", "", http.StatusOK)
	assertHTTPMetadataNumber(t, patchedSource.Metadata)
	var object map[string]json.RawMessage
	if err := json.Unmarshal(patchedSource.Metadata, &object); err != nil {
		t.Fatal(err)
	}
	var nested map[string]json.RawMessage
	if err := json.Unmarshal(object["nested"], &nested); err != nil || nested["remove"] != nil || nested["keep"] == nil {
		t.Fatalf("nested merge failed: %s %v", object["nested"], err)
	}
	searchURL := endpoint.URL + "/api/v2/collections/" + name + "/search"
	hit := metadataHTTPRequest(t, ctx, transport, http.MethodPost, searchURL, "",
		`{"query":"SciFact","mode":"hybrid","limit":10}`, http.StatusOK)
	if len(hit.Results) != 1 {
		t.Fatalf("search omitted source: %+v", hit)
	}
	assertHTTPMetadataNumber(t, hit.Results[0].Metadata)
	metadataHTTPRequest(t, ctx, transport, http.MethodPut, url, "",
		`{"content":"text","metadata":[]}`, http.StatusBadRequest)
	metadataHTTPRequest(t, ctx, transport, http.MethodPatch, url, created.ETag,
		`{"metadata":{"big":1}}`, http.StatusConflict)
	cleared := metadataHTTPRequest(t, ctx, transport, http.MethodPatch, url, patched.ETag,
		`{"metadata":null}`, http.StatusOK)
	fetched := metadataHTTPRequest(t, ctx, transport, http.MethodGet, url, "", "", http.StatusOK)
	if string(fetched.Metadata) != `{}` || fetched.Version != cleared.Version {
		t.Fatal("HTTP and stored metadata disagree after clear")
	}
}

type metadataHTTPReply struct {
	Raw      map[string]json.RawMessage `json:"-"`
	ETag     string                     `json:"-"`
	Version  string                     `json:"version"`
	Content  string                     `json:"content"`
	Limit    int                        `json:"limit"`
	Metadata json.RawMessage            `json:"metadata"`
	Results  []struct {
		ID       string          `json:"id"`
		Version  string          `json:"version"`
		Snippet  string          `json:"snippet"`
		Metadata json.RawMessage `json:"metadata"`
	} `json:"items"`
}

func metadataHTTPRequest(
	t *testing.T, ctx context.Context, client *http.Client, method, url, etag, body string, status int,
) metadataHTTPReply {
	t.Helper()
	request, err := http.NewRequestWithContext(ctx, method, url, bytes.NewBufferString(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	if etag != "" {
		request.Header.Set("If-Match", etag)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil || response.StatusCode != status {
		t.Fatalf("%s %s: status=%d want=%d body=%s err=%v", method, url, response.StatusCode, status, data, err)
	}
	var reply metadataHTTPReply
	if err := json.Unmarshal(data, &reply); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &reply.Raw); err != nil {
		t.Fatal(err)
	}
	reply.ETag = response.Header.Get("ETag")
	if reply.Version != "" && reply.ETag != strconv.Quote(reply.Version) {
		t.Fatal("metadata response omitted its conditional-write validator")
	}
	return reply
}

func TestHTTPV2SearchContractIntegration(t *testing.T) {
	requireJSONMetadataFixture(t)
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
		client.searchSvc.(*searchuc.Service), client.batchSvc.(*batchuc.Service), nil, nil, zap.NewNop())
	endpoint := httptest.NewServer(gen.Handler(server))
	defer endpoint.Close()
	transport := endpoint.Client()
	base := endpoint.URL + "/api/v2/collections/" + name
	content := "SciFact " + strings.Repeat("память🙂 ", 30)
	for i := range 13 {
		metadataHTTPRequest(t, ctx, transport, http.MethodPut,
			base+"/documents/"+strconv.Itoa(i), "",
			`{"content":`+strconv.Quote(content)+`,"metadata":{"nested":{"note":true}}}`, http.StatusCreated)
	}
	searchURL := base + "/search"
	var response metadataHTTPReply
	deadline := time.Now().Add(5 * time.Second)
	for {
		response = metadataHTTPRequest(t, ctx, transport, http.MethodPost, searchURL, "",
			`{"query":"SciFact","mode":"keyword","limit":100}`, http.StatusOK)
		if len(response.Results) == 13 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("index omitted sources: %+v", response)
		}
		time.Sleep(25 * time.Millisecond)
	}
	defaults := metadataHTTPRequest(t, ctx, transport, http.MethodPost, searchURL, "",
		`{"query":"SciFact","mode":"keyword"}`, http.StatusOK)
	if defaults.Limit != 10 || len(defaults.Results) != 10 {
		t.Fatalf("search defaults: limit=%d hits=%d", defaults.Limit, len(defaults.Results))
	}
	assertHTTPV2InvalidSearchControls(t, ctx, transport, searchURL)
	response = metadataHTTPRequest(t, ctx, transport, http.MethodPost, searchURL, "",
		`{"query":"SciFact","mode":"keyword","limit":100,"max_content_bytes":101}`, http.StatusOK)
	assertHTTPV2Snippets(t, &response, 13, 101)
	hit := response.Results[0]
	url := base + "/documents/" + hit.ID
	fetched := metadataHTTPRequest(t, ctx, transport, http.MethodGet, url, "", "", http.StatusOK)
	if fetched.Content != content || fetched.Version != hit.Version {
		t.Fatal("truncated search excerpt replaced the full source or its version")
	}
	updated := metadataHTTPRequest(t, ctx, transport, http.MethodPatch, url, strconv.Quote(hit.Version),
		`{"metadata":{"from_search":true}}`, http.StatusOK)
	if updated.Version == hit.Version {
		t.Fatal("search version did not support a conditional metadata update")
	}
	for _, field := range []string{"tags", "numerics", "revision"} {
		if _, exists := fetched.Raw[field]; exists {
			t.Fatalf("document exposed removed field %q", field)
		}
	}
}

func assertHTTPV2Snippets(t *testing.T, response *metadataHTTPReply, count, budget int) {
	t.Helper()
	if len(response.Results) != count {
		t.Fatalf("text budget dropped ranked IDs: %d", len(response.Results))
	}
	total := 0
	for _, hit := range response.Results {
		total += len(hit.Snippet)
		if hit.Version == "" || !utf8.ValidString(hit.Snippet) || !bytes.Contains(hit.Metadata, []byte("nested")) {
			t.Fatalf("missing or invalid source projection: %+v", hit)
		}
	}
	if total > budget {
		t.Fatalf("snippet bytes %d exceed %d", total, budget)
	}
	var items []map[string]json.RawMessage
	if err := json.Unmarshal(response.Raw["items"], &items); err != nil {
		t.Fatal(err)
	}
	for _, item := range items {
		for _, removed := range []string{"content", "tags", "numerics"} {
			if _, exists := item[removed]; exists {
				t.Fatalf("search exposed removed field %q", removed)
			}
		}
	}
}

func assertHTTPMetadataNumber(t *testing.T, raw json.RawMessage) {
	t.Helper()
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || string(fields["big"]) != "9007199254740993" {
		t.Fatalf("HTTP metadata lost a large JSON integer: %s %v", raw, err)
	}
}

func assertHTTPV2InvalidSearchControls(
	t *testing.T, ctx context.Context, transport *http.Client, searchURL string,
) {
	t.Helper()
	for _, body := range []string{
		`{"query":"SciFact","limit":0}`, `{"query":"SciFact","limit":101}`,
		`{"query":"SciFact","max_content_bytes":0}`, `{"query":"SciFact","top_k":10}`,
	} {
		metadataHTTPRequest(t, ctx, transport, http.MethodPost, searchURL, "", body, http.StatusBadRequest)
	}
}

func assertCompactHTTPWriteResult(t *testing.T, reply *metadataHTTPReply) {
	t.Helper()
	for _, field := range []string{"content", "metadata", "tags", "numerics"} {
		if _, present := reply.Raw[field]; present {
			t.Fatalf("write receipt duplicated source field %q", field)
		}
	}
	if reply.Version == "" || reply.Raw["id"] == nil || reply.Raw["created"] == nil {
		t.Fatal("write receipt omitted its source identity or outcome")
	}
}
