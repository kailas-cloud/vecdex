package mcp

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/kailas-cloud/vecdex/internal/domain/storage"

	domdoc "github.com/kailas-cloud/vecdex/internal/domain/document"
	domusage "github.com/kailas-cloud/vecdex/internal/domain/usage"
	"github.com/kailas-cloud/vecdex/internal/usecase/health"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	domcol "github.com/kailas-cloud/vecdex/internal/domain/collection"
	"github.com/kailas-cloud/vecdex/internal/domain/metadata"
	"github.com/kailas-cloud/vecdex/internal/memorycontext"
)

func TestEveryToolOutputMatchesDeclaredSchema(t *testing.T) {
	h, _, _ := fixture(t)
	h.deps.Usage = testUsage{}
	h.deps.Health = testHealth{}
	cases := []struct {
		name string
		args map[string]any
	}{
		{"memory_search", map[string]any{"context_id": "context-1", "query": "facts"}},
		{"memory_similar", map[string]any{"context_id": "context-1", "id": "doc"}},
		{"memory_read", map[string]any{"context_id": "context-1", "id": "doc", "include_metadata": true}},
		{"memory_list", map[string]any{"context_id": "context-1"}},
		{"memory_create",
			map[string]any{"context_id": "context-1",
				"id":              "new",
				"content":         "new fact",
				"idempotency_key": "create"}},

		{"memory_update",
			map[string]any{"context_id": "context-1",
				"id":              "doc",
				"content":         "changed",
				"if_version":      "source-v1",
				"idempotency_key": "update"}},

		{"memory_delete",
			map[string]any{"context_id": "context-1",
				"id":              "doc",
				"if_version":      "source-v1",
				"idempotency_key": "delete"}},

		{"collection_list", map[string]any{}},
		{"collection_get", map[string]any{"name": "notes"}},
		{"collection_create", map[string]any{"name": "new"}},
		{"collection_delete", map[string]any{"name": "notes", "generation": "gen-1", "confirm_name": "notes"}},
		{"context_list", map[string]any{}},
		{"context_create",
			map[string]any{"name": "notes",
				"collection":      "notes",
				"generation":      "gen-1",
				"idempotency_key": "context-create"}},

		{"context_get", map[string]any{"context_id": "context-1", "verify_pins": true}},
		{"context_update",
			map[string]any{"context_id": "context-1",
				"name":             "changed",
				"expected_version": "context-v1",
				"idempotency_key":  "context-update"}},

		{"context_delete",
			map[string]any{"context_id": "context-1",
				"expected_version": "context-v1",
				"idempotency_key":  "context-delete"}},

		{"memory_status", map[string]any{"context_id": "context-1"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			callTool(t, h, Principal{"owner", "admin"}, latestVersion, tc.name, tc.args)
		})
	}
}

func TestMetadataProjectionAndPinVerification(t *testing.T) {
	h, docs, _ := fixture(t)
	md, err := metadata.Parse([]byte(`{"keep":"value","omit":"private","large":"detail"}`))
	if err != nil {
		t.Fatal(err)
	}
	docs.doc.SetMetadata(md)
	got := callTool(t, h, Principal{"owner", "read"}, latestVersion, "memory_read", map[string]any{
		"context_id": "context-1", "id": "doc", "include_content": false, "metadata_fields": []string{"keep"},
	})["data"].(map[string]any)
	if _, ok := got["content"]; ok {
		t.Fatal("metadata projection returned content")
	}
	if !bytes.Equal(jsonBytes(got["metadata"]), []byte(`{"keep":"value"}`)) {
		t.Fatalf("unexpected metadata: %#v", got)
	}
	contexts := h.deps.Contexts.(*testContexts)
	contexts.c.Pins = []memorycontext.Pin{{DocumentID: "doc",
		Version: "source-v1"},
		{DocumentID: "doc",
			Version: "old"},
		{DocumentID: "missing",
			Version: "old"}}
	got = callTool(t,
		h,
		Principal{"owner",
			"read"},
		latestVersion,
		"context_get",
		map[string]any{"context_id": "context-1",
			"verify_pins": true})["data"].(map[string]any)
	states := got["pin_states"].([]any)
	for i, want := range []string{"current", "stale", "missing"} {
		if states[i].(map[string]any)["state"] != want {
			t.Fatalf("pin states: %#v", states)
		}
	}
}

func TestExpiryPresenceAndPerRequestBudgets(t *testing.T) {
	for _, tc := range []struct {
		raw     string
		present bool
		want    int64
	}{{"", false, 0}, {"null", true, 0}, {"0", true, 0}, {"123", true, 123}} {
		var raw json.RawMessage
		if tc.raw != "" {
			raw = json.RawMessage(tc.raw)
		}
		got, err := parseSourceExpiry(raw)
		if err != nil || (got != nil) != tc.present || got != nil && *got != tc.want {
			t.Fatalf("%q: %v %v", tc.raw, got, err)
		}
	}
	h, _, _ := fixture(t)
	for _, budget := range []int{4096, 32768, 256 << 10} {
		wire, w := callRPC(t, h, Principal{"owner", "read"}, latestVersion, "tools/call", map[string]any{
			"name": "memory_read",
			"arguments": map[string]any{"context_id": "context-1",
				"id":               "doc",
				"max_output_bytes": budget},
		})
		if len(jsonBytes(wire)) > budget {
			t.Fatalf("wire budget exceeded: %d>%d: %s", len(jsonBytes(wire)), budget, w.Body.String())
		}
	}
}

type failedCollections struct{ CollectionService }

func (failedCollections) Get(context.Context, string) (domcol.Collection, error) {
	return domcol.Collection{}, errors.New("backend unavailable")
}
func TestDependencyFailureDoesNotInvalidateContext(t *testing.T) {
	h, _, _ := fixture(t)
	h.deps.Collections = failedCollections{h.deps.Collections}
	body := callTool(t,
		h,
		Principal{"owner",
			"read"},
		latestVersion,
		"memory_read",
		map[string]any{"context_id": "context-1",
			"id": "doc"})
	if body["error"].(map[string]any)["code"] != "internal_error" {
		t.Fatalf("wrong recovery: %#v", body)
	}
}
func TestSubscriptionChecksAreSharedAndOwnerScoped(t *testing.T) {
	h, _, _ := fixture(t)
	ctx := WithPrincipal(t.Context(), Principal{"owner", "read"})
	uris := []string{documentURI("notes", "gen-1", "doc")}
	first, err := h.watchResources(ctx, uris)
	if err != nil {
		t.Fatal(err)
	}
	second, err := h.watchResources(ctx, uris)
	if err != nil {
		t.Fatal(err)
	}
	h.watchMu.Lock()
	count := len(h.watches)
	refs := h.watches[uris[0]].refs
	h.watchMu.Unlock()
	if count != 1 || refs != 2 {
		t.Fatalf("not shared: %d %d", count, refs)
	}
	first()
	second()
	h.watchMu.Lock()
	count = len(h.watches)
	h.watchMu.Unlock()
	if count != 0 {
		t.Fatal("watch leaked after disconnect")
	}
	_, err = h.watchResources(WithPrincipal(t.Context(), Principal{"other", "read"}), []string{contextURI("context-1")})
	if err == nil {
		t.Fatal("foreign owner context subscription accepted")
	}
}

func TestContractCatalogGolden(t *testing.T) {
	raw, err := json.MarshalIndent(Contracts(), "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	raw = append(raw, '\n')
	const path = "testdata/contracts.json"
	if os.Getenv("UPDATE_MCP_CONTRACTS") == "1" {
		if err := os.MkdirAll("testdata", 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	golden, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(raw, golden) {
		t.Fatal("tool contracts changed: review schemas then run UPDATE_MCP_CONTRACTS=1 go test " +
			"./internal/transport/mcp -run TestContractCatalogGolden")
	}
}

type testUsage struct{}

func (testUsage) GetReport(context.Context, domusage.Period) (domusage.Report, error) {
	return domusage.Report{}, nil
}

type testHealth struct{}

func (testHealth) Check(context.Context) health.Report {
	return health.Report{Status: health.Healthy,
		Checks: map[string]health.CheckResult{"valkey": health.CheckOK}}
}
func TestEscapedReadPagesRespectFinalWireBudget(t *testing.T) {
	h, docs, _ := fixture(t)
	doc, err := domdoc.New("doc", strings.Repeat("\x00\"\n", 4000), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	doc.SetVersion("escaped-v1")
	docs.doc = doc
	wire, _ := callRPC(t, h, Principal{"owner", "read"}, latestVersion, "tools/call", map[string]any{
		"name": "memory_read", "arguments": map[string]any{"context_id": "context-1", "id": "doc", "max_output_bytes": 4096},
	})
	result := wire["result"].(map[string]any)
	body := result["structuredContent"].(map[string]any)
	if body["ok"] != true || len(jsonBytes(wire)) > 4096 {
		t.Fatalf("budget/page failed: %#v", wire)
	}
	data := body["data"].(map[string]any)
	if data["next_offset"].(float64) <= 0 {
		t.Fatal("page made no progress")
	}
}
func TestContradictoryRangeIsAgentInputError(t *testing.T) {
	h, _, _ := fixture(t)
	body := callTool(t, h, Principal{"owner", "read"}, latestVersion, "memory_search", map[string]any{
		"context_id": "context-1",
		"query":      "fact",
		"filters": map[string]any{"must": []any{map[string]any{"key": "priority",
			"range": map[string]any{"gte": 10,
				"lt": 1}}}},
	})
	if body["error"].(map[string]any)["code"] != "invalid_argument" {
		t.Fatalf("wrong range recovery: %#v", body)
	}
}

func TestOwnerRelativeNotificationsNeverCrossPrincipals(t *testing.T) {
	h, _, _ := fixture(t)
	alice, bob := &sdk.ServerSession{}, &sdk.ServerSession{}
	h.listenerOwners.Store(alice, "alice")
	h.listenerOwners.Store(bob, "bob")
	delivered := 0
	send := h.isolateOwnerNotifications(func(_ context.Context, _ string, req sdk.Request) (sdk.Result, error) {
		delivered++
		if _, leaked := req.GetParams().GetMeta()[notificationOwnerKey]; leaked {
			t.Fatal("internal owner marker leaked")
		}
		if req.GetParams().GetMeta()[sdk.MetaKeySubscriptionID] != 1 {
			t.Fatal("subscription routing removed")
		}
		return &sdk.CallToolResult{}, nil
	})
	for _, session := range []*sdk.ServerSession{alice, bob} {
		p := &sdk.ResourceUpdatedNotificationParams{URI: "vecdex://contexts"}
		p.SetMeta(map[string]any{notificationOwnerKey: "alice", sdk.MetaKeySubscriptionID: 1})
		_,
			err := send(t.Context(),
			"notifications/resources/updated",
			&sdk.ServerRequest[*sdk.ResourceUpdatedNotificationParams]{Session: session,
				Params: p})
		if err != nil {
			t.Fatal(err)
		}
	}
	if delivered != 1 {
		t.Fatalf("delivered to %d principals", delivered)
	}
}

type pagedContexts struct{ ContextService }

func (p pagedContexts) List(ctx context.Context, r memorycontext.ListRequest) (memorycontext.Page, error) {
	page, err := p.ContextService.List(ctx, r)
	page.NextCursor = "next-page"
	return page, err
}
func TestCompletionsValidateReferencesAndReportMore(t *testing.T) {
	h, _, _ := fixture(t)
	h.deps.Contexts = pagedContexts{h.deps.Contexts}
	params := map[string]any{"ref": map[string]any{"type": "ref/prompt",
		"name": "resume_context"},
		"argument": map[string]any{"name": "context_id",
			"value": "context"}}
	wire, _ := callRPC(t, h, Principal{"owner", "read"}, latestVersion, "completion/complete", params)
	completion := wire["result"].(map[string]any)["completion"].(map[string]any)
	if completion["hasMore"] != true {
		t.Fatalf("partial completion claimed complete: %#v", completion)
	}
	_,
		err := h.complete(WithPrincipal(t.Context(),
		Principal{"owner",
			"read"}),
		&sdk.CompleteRequest{Params: &sdk.CompleteParams{Ref: &sdk.CompleteReference{Type: "ref/prompt",
			Name: "unknown"}}})
	if err == nil {
		t.Fatal("unknown completion reference accepted")
	}
}

func TestLongNativeDocumentCursorPassesToolContract(t *testing.T) {
	h, _, _ := fixture(t)
	layout, err := storage.NewCollection("notes", "gen-1")
	if err != nil {
		t.Fatal(err)
	}
	cursor := "d3." + base64.RawURLEncoding.EncodeToString([]byte(layout.Base()+"\x00"+strings.Repeat("d", 256)))
	if len(cursor) <= 256 {
		t.Fatal("regression fixture cursor is too short")
	}
	body := callTool(t,
		h,
		Principal{"owner",
			"read"},
		latestVersion,
		"memory_list",
		map[string]any{"context_id": "context-1",
			"cursor": cursor})
	if body["ok"] != true {
		t.Fatalf("valid native cursor rejected: %#v", body)
	}
}

func TestMixedCaseResourceSchemePreservesOwnerIsolation(t *testing.T) {
	h, _, _ := fixture(t)
	uri := "VECDEx://contexts"
	if !ownerScopedURI(uri) {
		t.Fatal("parsed context URI lost owner scope")
	}
	for _, owner := range []string{"alice", "bob"} {
		release, err := h.watchResources(WithPrincipal(t.Context(), Principal{owner, "read"}), []string{uri})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(release)
	}
	h.watchMu.Lock()
	defer h.watchMu.Unlock()
	if len(h.watches) != 2 {
		t.Fatalf("owner catalogs shared a watch: %d", len(h.watches))
	}
	for _, owner := range []string{"alice", "bob"} {
		if h.watches[owner+"\x00"+uri] == nil {
			t.Fatalf("missing scoped watch for %s", owner)
		}
	}
}

func TestCrossOriginProtectionWithAuthenticatedRequests(t *testing.T) {
	h, _, _ := fixture(t)
	for _, tc := range []struct {
		origin string
		status int
	}{{"",
		http.StatusOK},
		{"http://localhost",
			http.StatusOK},
		{"https://foreign.invalid",
			http.StatusForbidden}} {
		t.Run(tc.origin, func(t *testing.T) {
			params := map[string]any{}
			r := httptest.NewRequestWithContext(t.Context(),
				http.MethodPost,
				"http://localhost/mcp",
				bytes.NewReader(rpcBody(latestVersion,
					"tools/list",
					params)))
			rpcHeaders(r, latestVersion, "tools/list", params)
			if tc.origin != "" {
				r.Header.Set("Origin", tc.origin)
			}
			r = r.WithContext(WithPrincipal(r.Context(), Principal{"owner", "admin"}))
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tc.status {
				t.Fatalf("Origin %q: HTTP %d, want %d", tc.origin, w.Code, tc.status)
			}
		})
	}
}
