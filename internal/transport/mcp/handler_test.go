package mcp

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/kailas-cloud/vecdex/internal/domain"
	domcol "github.com/kailas-cloud/vecdex/internal/domain/collection"
	"github.com/kailas-cloud/vecdex/internal/domain/collection/field"
	domdoc "github.com/kailas-cloud/vecdex/internal/domain/document"
	"github.com/kailas-cloud/vecdex/internal/domain/document/patch"
	"github.com/kailas-cloud/vecdex/internal/domain/metadata"
	"github.com/kailas-cloud/vecdex/internal/domain/search/request"
	"github.com/kailas-cloud/vecdex/internal/domain/search/result"
	"github.com/kailas-cloud/vecdex/internal/memorycontext"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

type testCollections struct {
	mu  sync.Mutex
	col domcol.Collection
}

func (f *testCollections) Get(context.Context, string) (domcol.Collection, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.col, nil
}
func (f *testCollections) ListPage(context.Context, string, int) ([]domcol.Collection, string, error) {
	c, _ := f.Get(context.Background(), "")
	return []domcol.Collection{c}, "", nil
}
func (f *testCollections) Inspect(ctx context.Context, name string) (domcol.Inspection, error) {
	c, _ := f.Get(ctx, name)
	return domcol.Inspection{Name: c.Name(), Generation: c.Generation(), State: "active", Collection: &c}, nil
}
func (f *testCollections) Create(_ context.Context,
	name string,
	kind domcol.Type,
	fields []field.Field) (domcol.Collection,
	error) {
	if name == f.col.Name() {
		return domcol.Collection{}, domain.ErrAlreadyExists
	}
	return domcol.New(name, kind, fields, 3)
}

func (f *testCollections) DeleteIfGeneration(_ context.Context, _, generation string) error {
	c, _ := f.Get(context.Background(), "")
	if generation != c.Generation() {
		return domain.ErrCollectionGenerationMismatch
	}
	return nil
}

type testDocuments struct {
	mu                                          sync.Mutex
	doc                                         domdoc.Document
	writes                                      int
	expectedVersion, idempotencyKey, generation string
	block                                       bool
}

func (f *testDocuments) ReadSource(ctx context.Context,
	_ string,
	id string,
	_ domdoc.SourceProjection) (domdoc.Document,
	error) {
	f.mu.Lock()
	blocked := f.block
	doc := f.doc
	f.mu.Unlock()
	if blocked {
		<-ctx.Done()
		return domdoc.Document{}, ctx.Err()
	}
	if doc.ID() != id {
		return domdoc.Document{}, domain.ErrDocumentNotFound
	}
	return doc, nil
}
func (f *testDocuments) ListSources(ctx context.Context,
	col,
	_ string,
	_ int,
	p domdoc.SourceProjection) ([]domdoc.Document,
	string,
	error) {
	d, err := f.ReadSource(ctx, col, "doc", p)
	return []domdoc.Document{d}, "", err
}
func (f *testDocuments) Upsert(ctx context.Context, _ string, doc *domdoc.Document) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.writes++
	f.idempotencyKey = domain.IdempotencyKey(ctx)
	f.generation = domain.ExpectedCollectionGeneration(ctx)
	if f.doc.ID() == doc.ID() {
		return false, domain.ErrAlreadyExists
	}
	doc.SetVersion("created-v1")
	return true, nil
}
func (f *testDocuments) Patch(ctx context.Context, _, id string, _ patch.Patch) (domdoc.WriteResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.writes++
	f.idempotencyKey = domain.IdempotencyKey(ctx)
	f.generation = domain.ExpectedCollectionGeneration(ctx)
	if v := domain.ExpectedVersion(ctx); v != nil {
		f.expectedVersion = *v
	}
	if f.expectedVersion != f.doc.Version() {
		return domdoc.WriteResult{}, domain.NewVersionConflict(f.doc.Version())
	}
	return domdoc.WriteResult{ID: id, Version: "updated-v2"}, nil
}
func (f *testDocuments) Delete(ctx context.Context, _, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.writes++
	if v := domain.ExpectedVersion(ctx); v == nil {
		return fmt.Errorf("missing CAS")
	}
	return nil
}

type testSearch struct{}

func (testSearch) Search(context.Context, string, *request.Request) ([]result.Result, int, error) {
	return []result.Result{result.New("doc",
			0.9,
			"evidence",
			nil,
			nil,
			nil).WithSource("source-v1",
			metadata.Object{})},
		1,
		nil
}
func (testSearch) Similar(context.Context, string, string, *request.SimilarRequest) ([]result.Result, int, error) {
	return []result.Result{}, 0, nil
}

type testContexts struct{ c memorycontext.Context }

func (f *testContexts) Get(_ context.Context, owner, id string) (memorycontext.Context, error) {
	if owner != f.c.Owner || id != f.c.ID {
		return memorycontext.Context{}, memorycontext.ErrNotFound
	}
	return f.c, nil
}
func (f *testContexts) List(_ context.Context, r memorycontext.ListRequest) (memorycontext.Page, error) {
	items := []memorycontext.Context{}
	if r.Owner == f.c.Owner {
		items = append(items, f.c)
	}
	return memorycontext.Page{Contexts: items}, nil
}

//nolint:gocritic // The fake implements the service API value request.
func (f *testContexts) Create(ctx context.Context,
	r memorycontext.CreateRequest) (memorycontext.MutationResult,
	error) {
	return f.CreateValidated(ctx, r, nil)
}

//nolint:gocritic // The fake implements the service API value request.
func (f *testContexts) CreateValidated(ctx context.Context,
	_ memorycontext.CreateRequest,
	validate func(context.Context) error) (memorycontext.MutationResult,
	error) {
	if validate != nil {
		if err := validate(ctx); err != nil {
			return memorycontext.MutationResult{}, err
		}
	}
	return memorycontext.MutationResult{Context: f.c,
			Operation:      "create",
			CurrentState:   "present",
			CurrentVersion: f.c.Version},
		nil
}
func (f *testContexts) Update(context.Context, memorycontext.UpdateRequest) (memorycontext.MutationResult, error) {
	return memorycontext.MutationResult{Context: f.c,
			Operation:      "update",
			CurrentState:   "present",
			CurrentVersion: f.c.Version},
		nil
}
func (f *testContexts) Delete(context.Context, memorycontext.DeleteRequest) (memorycontext.MutationResult, error) {
	return memorycontext.MutationResult{Context: f.c, Operation: "delete", CurrentState: "absent"}, nil
}

func fixture(t *testing.T) (*Handler, *testDocuments, *testCollections) {
	t.Helper()
	col, err := domcol.New("notes", domcol.TypeText, nil, 3)
	if err != nil {
		t.Fatal(err)
	}
	col = col.WithGeneration("gen-1")
	doc, err := domdoc.New("doc", "hello 🌍\ntrusted evidence", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	doc.SetVersion("source-v1")
	docs := &testDocuments{doc: doc}
	cols := &testCollections{col: col}
	contexts := &testContexts{c: memorycontext.Context{ID: "context-1",
		Version:    "context-v1",
		Owner:      "owner",
		Name:       "notes",
		Collection: "notes",
		Generation: "gen-1",
		Search:     json.RawMessage(`{}`),
		Pins:       []memorycontext.Pin{}}}
	h,
		err := New(Dependencies{Collections: cols,
		Documents: docs,
		Search:    testSearch{},
		Contexts:  contexts},
		Options{Version: "test",
			PollInterval: 20 * time.Millisecond,
			Skills: []Skill{{Name: "remember",
				Description: "Remember facts",
				Content:     "---\nname: remember\ndescription: Remember facts\n---\nUse memory tools.\n",
				Files:       map[string][]byte{"references/example.md": []byte("# Example\n")}}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h.Close() })
	return h, docs, cols
}

func rpcBody(version, method string, params map[string]any) []byte {
	if params == nil {
		params = map[string]any{}
	}
	if version == latestVersion {
		params["_meta"] = map[string]any{sdk.MetaKeyProtocolVersion: version,
			sdk.MetaKeyClientInfo: map[string]any{"name": "test",
				"version": "1"},
			sdk.MetaKeyClientCapabilities: map[string]any{}}
	}
	return jsonBytes(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
}

func rpcHeaders(r *http.Request, version, method string, params map[string]any) {
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Accept", "application/json, text/event-stream")
	r.Header.Set("MCP-Protocol-Version", version)
	if version == latestVersion {
		r.Header.Set("Mcp-Method", method)
		if method == "tools/call" || method == "prompts/get" {
			name, _ := params["name"].(string)
			r.Header.Set("Mcp-Name", name)
		}
		if method == "resources/read" {
			uri, _ := params["uri"].(string)
			r.Header.Set("Mcp-Name", uri)
		}
	}
}

func callRPC(t *testing.T,
	h *Handler,
	p Principal,
	version,
	method string,
	params map[string]any) (map[string]any,
	*httptest.ResponseRecorder) {
	t.Helper()
	r := httptest.NewRequestWithContext(t.Context(),
		http.MethodPost,
		"http://localhost/mcp",
		bytes.NewReader(rpcBody(version,
			method,
			params)))
	rpcHeaders(r, version, method, params)
	r = r.WithContext(WithPrincipal(r.Context(), p))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("%s HTTP %d: %s", method, w.Code, w.Body.String())
	}
	var response map[string]any
	for _, line := range strings.Split(w.Body.String(), "\n") {
		line = strings.TrimPrefix(line, "data: ")
		if !strings.HasPrefix(line, "{") {
			continue
		}
		var message map[string]any
		if json.Unmarshal([]byte(line), &message) != nil {
			continue
		}
		if _, ok := message["id"]; ok {
			response = message
		}
	}
	if response == nil {
		t.Fatalf("missing response to %s: %s", method, w.Body.String())
	}
	return response, w
}

func callTool(t *testing.T, h *Handler, p Principal, version, name string, args map[string]any) map[string]any {
	t.Helper()
	wire, _ := callRPC(t, h, p, version, "tools/call", map[string]any{"name": name, "arguments": args})
	res, ok := wire["result"].(map[string]any)
	if !ok {
		t.Fatalf("missing tool result: %#v", wire)
	}
	structured, ok := res["structuredContent"].(map[string]any)
	if !ok {
		t.Fatalf("missing structured result: %#v", res)
	}
	contents := res["content"].([]any)
	text := contents[0].(map[string]any)["text"].(string)
	var fallback map[string]any
	if err := json.Unmarshal([]byte(text), &fallback); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(jsonBytes(fallback), jsonBytes(structured)) {
		t.Fatalf("text and structured results differ: %s %#v", text, structured)
	}
	if (structured["ok"] == false) != (res["isError"] == true) {
		t.Fatalf("inconsistent isError: %#v", res)
	}
	var schema jsonschema.Schema
	if err := json.Unmarshal(jsonBytes(toolOutputSchema(name)), &schema); err != nil {
		t.Fatal(err)
	}
	resolved, err := schema.Resolve(nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := resolved.Validate(structured); err != nil {
		t.Fatalf("%s output schema: %v; result: %#v", name, err, structured)
	}
	return structured
}

//nolint:cyclop // This matrix checks both protocol profiles and all three authorization roles.
func TestWireProfilesAndRoleDiscovery(t *testing.T) {
	h, _, _ := fixture(t)
	for _, version := range []string{latestVersion, legacyVersion} {
		for _, role := range []string{"read", "write", "admin"} {
			t.Run(version+"/"+role, func(t *testing.T) {
				p := Principal{KeyID: "owner", Role: role}
				method := "server/discover"
				params := map[string]any{}
				if version == legacyVersion {
					method = "initialize"
					params = map[string]any{"protocolVersion": version,
						"clientInfo": map[string]any{"name": "test",
							"version": "1"},
						"capabilities": map[string]any{}}
				}
				wire, w := callRPC(t, h, p, version, method, params)
				res := wire["result"].(map[string]any)
				caps := res["capabilities"].(map[string]any)
				if w.Header().Get("Mcp-Session-Id") != "" {
					t.Fatal("stateless response has session ID")
				}
				resources := caps["resources"].(map[string]any)
				if version == latestVersion {
					if resources["subscribe"] != true || caps["extensions"] == nil {
						t.Fatalf("missing modern capabilities: %#v", caps)
					}
				} else if resources["subscribe"] == true || caps["extensions"] != nil {
					t.Fatalf("false legacy capabilities: %#v", caps)
				}
				listing := []any{}
				cursor := ""
				for {
					params := map[string]any{}
					if cursor != "" {
						params["cursor"] = cursor
					}
					tools, _ := callRPC(t, h, p, version, "tools/list", params)
					page := tools["result"].(map[string]any)
					listing = append(listing, page["tools"].([]any)...)
					cursor, _ = page["nextCursor"].(string)
					if cursor == "" {
						break
					}
				}
				want := map[string]int{"read": 12, "write": 15, "admin": 17}[role]
				if len(listing) != want {
					t.Fatalf("got %d tools want %d", len(listing), want)
				}
				for _, item := range listing {
					tool := item.(map[string]any)
					if !allows(role, toolRoles[tool["name"].(string)]) {
						t.Fatalf("forbidden discovery %#v", tool)
					}
				}
			})
		}
	}
}

func TestToolAuthorizationCASAndGeneration(t *testing.T) {
	h, docs, cols := fixture(t)
	reader := Principal{KeyID: "owner", Role: "read"}
	writer := Principal{KeyID: "owner", Role: "write"}
	args := map[string]any{"context_id": "context-1",
		"id":              "doc",
		"content":         "changed",
		"if_version":      "source-v1",
		"idempotency_key": "update-1"}
	denied := callTool(t, h, reader, latestVersion, "memory_update", args)
	if denied["error"].(map[string]any)["code"] != "forbidden" {
		t.Fatalf("not forbidden: %#v", denied)
	}
	if docs.writes != 0 {
		t.Fatal("unauthorized operation reached engine")
	}
	ok := callTool(t, h, writer, latestVersion, "memory_update", args)
	if ok["ok"] != true {
		t.Fatalf("write failed: %#v", ok)
	}
	if docs.expectedVersion != "source-v1" || docs.generation != "gen-1" || docs.idempotencyKey == "" {
		t.Fatalf("write guards lost: %#v", docs)
	}
	args["if_version"] = "stale"
	conflict := callTool(t, h, writer, legacyVersion, "memory_update", args)
	if conflict["error"].(map[string]any)["current_version"] != "source-v1" {
		t.Fatalf("CAS error lost details: %#v", conflict)
	}
	cols.mu.Lock()
	cols.col = cols.col.WithGeneration("gen-2")
	cols.mu.Unlock()
	stale := callTool(t, h, reader, latestVersion, "memory_read", map[string]any{"context_id": "context-1", "id": "doc"})
	if stale["error"].(map[string]any)["code"] != "stale_context" {
		t.Fatalf("old context retargeted: %#v", stale)
	}
	other := callTool(t,
		h,
		Principal{KeyID: "other",
			Role: "admin"},
		latestVersion,
		"context_get",
		map[string]any{"context_id": "context-1"})
	if other["error"].(map[string]any)["code"] != "not_found" {
		t.Fatalf("context owner bypass: %#v", other)
	}
}

func TestReadPagingAndInputValidation(t *testing.T) {
	h, _, _ := fixture(t)
	p := Principal{KeyID: "owner", Role: "read"}
	first := callTool(t,
		h,
		p,
		latestVersion,
		"memory_read",
		map[string]any{"context_id": "context-1",
			"id":        "doc",
			"max_bytes": 8})
	data := first["data"].(map[string]any)
	if data["content"] != "hello " || !utf8.ValidString(data["content"].(string)) {
		t.Fatalf("invalid UTF8 page: %#v", data)
	}
	bad := callTool(t,
		h,
		p,
		latestVersion,
		"memory_read",
		map[string]any{"context_id": "context-1",
			"id":     "doc",
			"offset": 6})
	if bad["ok"] != false {
		t.Fatal("continuation did not require version")
	}
	second := callTool(t,
		h,
		p,
		latestVersion,
		"memory_read",
		map[string]any{"context_id": "context-1",
			"id":      "doc",
			"offset":  6,
			"version": "source-v1"})
	if !strings.HasPrefix(second["data"].(map[string]any)["content"].(string), "🌍") {
		t.Fatalf("continuation wrong: %#v", second)
	}
	unknown := callTool(t,
		h,
		p,
		latestVersion,
		"memory_search",
		map[string]any{"context_id": "context-1",
			"query":           "facts",
			"include_vectors": true})
	if unknown["error"].(map[string]any)["code"] != "invalid_argument" {
		t.Fatal("unknown input accepted")
	}
	missing := callTool(t, h, p, latestVersion, "memory_read", map[string]any{"id": "doc"})
	if missing["ok"] != false {
		t.Fatal("context was optional")
	}
}

func TestSkillsResourcesPromptsAndCache(t *testing.T) {
	h, _, _ := fixture(t)
	p := Principal{KeyID: "owner", Role: "write"}
	wire, _ := callRPC(t, h, p, latestVersion, "skills/list", nil)
	res := wire["result"].(map[string]any)
	if res["resultType"] != "complete" || res["cacheScope"] != "private" || res["ttlMs"] != float64(300000) {
		t.Fatalf("bad skills envelope: %#v", res)
	}
	entry := res["skills"].([]any)[0].(map[string]any)
	uri := entry["uri"].(string)
	get, _ := callRPC(t, h, p, latestVersion, "skills/get", map[string]any{"uri": uri})
	if !bytes.Equal(jsonBytes(get["result"].(map[string]any)["skill"]), jsonBytes(entry)) {
		t.Fatal("skills list/get mismatch")
	}
	read, _ := callRPC(t, h, p, latestVersion, "resources/read", map[string]any{"uri": uri})
	resource := read["result"].(map[string]any)
	text := resource["contents"].([]any)[0].(map[string]any)["text"].(string)
	digest := sha256.Sum256([]byte(text))
	manifest := entry["resources"].([]any)[0].(map[string]any)
	if manifest["digest"] != "sha256:"+hex.EncodeToString(digest[:]) {
		t.Fatal("skill digest does not match exact bytes")
	}
	if resource["cacheScope"] != "private" {
		t.Fatal("resource publicly cacheable")
	}
	old, _ := callRPC(t, h, p, legacyVersion, "skills/list", nil)
	if old["error"] == nil {
		t.Fatal("legacy wire accepted skills extension")
	}
	prompt,
		_ := callRPC(t,
		h,
		p,
		latestVersion,
		"prompts/get",
		map[string]any{"name": "resume_context",
			"arguments": map[string]any{"context_id": "context-1",
				"topic": "hello"}})
	if prompt["error"] != nil {
		t.Fatalf("prompt failed: %#v", prompt)
	}
	completion,
		_ := callRPC(t,
		h,
		p,
		latestVersion,
		"completion/complete",
		map[string]any{"ref": map[string]any{"type": "ref/prompt",
			"name": "resume_context"},
			"argument": map[string]any{"name": "context_id",
				"value": "context"}})
	values := completion["result"].(map[string]any)["completion"].(map[string]any)["values"].([]any)
	if len(values) != 1 || values[0] != "context-1" {
		t.Fatalf("completion: %#v", values)
	}
}

func TestSubscriptionInvalidationAndCancellation(t *testing.T) {
	h, docs, _ := fixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	uri := documentURI("notes", "gen-1", "doc")
	params := map[string]any{"notifications": map[string]any{"resourceSubscriptions": []string{uri}}}
	r,
		err := http.NewRequestWithContext(ctx,
		http.MethodPost,
		"http://localhost/mcp",
		bytes.NewReader(rpcBody(latestVersion,
			"subscriptions/listen",
			params)))
	if err != nil {
		t.Fatal(err)
	}
	rpcHeaders(r, latestVersion, "subscriptions/listen", params)
	r = r.WithContext(WithPrincipal(r.Context(), Principal{KeyID: "owner", Role: "read"}))
	reader, writer := io.Pipe()
	defer reader.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer writer.Close()
		h.ServeHTTP(&pipeResponse{header: http.Header{}, writer: writer}, r)
	}()
	scanner := bufio.NewScanner(reader)
	if !nextEvent(t, scanner, "notifications/subscriptions/acknowledged") {
		t.Fatal("missing first subscription acknowledgement")
	}
	docs.mu.Lock()
	docs.doc.SetVersion("source-v2")
	docs.mu.Unlock()
	if !nextEvent(t, scanner, "notifications/resources/updated") {
		t.Fatal("missing resource invalidation")
	}
	cancel()
	_, _ = io.Copy(io.Discard, reader)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("listener did not stop after cancellation")
	}
}

type pipeResponse struct {
	header http.Header
	writer *io.PipeWriter
}

func (p *pipeResponse) Header() http.Header            { return p.header }
func (*pipeResponse) WriteHeader(int)                  {}
func (p *pipeResponse) Write(data []byte) (int, error) { return p.writer.Write(data) }
func (*pipeResponse) Flush()                           {}

func nextEvent(t *testing.T, scanner *bufio.Scanner, method string) bool {
	t.Helper()
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var event map[string]any
		if json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event) != nil {
			continue
		}
		return event["method"] == method
	}
	return false
}

func TestBudgetsAuthenticationAndLegacySubscriptions(t *testing.T) {
	h, _, _ := fixture(t)
	res := h.boundedTool(map[string]any{"text": strings.Repeat("\"🌍", h.opts.MaxResultBytes)}, nil)
	raw := jsonBytes(res)
	if len(raw)+h.protocolReserve > h.opts.MaxResultBytes || !res.IsError {
		t.Fatal("unbounded result or no error")
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequestWithContext(t.Context(), http.MethodPost, "http://localhost/mcp", http.NoBody))
	if w.Code != http.StatusUnauthorized {
		t.Fatal("missing principal accepted")
	}
	p := Principal{KeyID: "owner", Role: "read"}
	wire, _ := callRPC(t, h, p, legacyVersion, "resources/subscribe", map[string]any{"uri": "vecdex://status"})
	if wire["error"] == nil {
		t.Fatal("legacy subscription falsely succeeded")
	}
	r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "http://localhost/mcp", http.NoBody)
	r = r.WithContext(WithPrincipal(r.Context(), p))
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET status %d", w.Code)
	}
}
