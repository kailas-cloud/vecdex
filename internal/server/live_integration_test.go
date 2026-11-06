package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/kailas-cloud/vecdex/internal/app"
	"github.com/kailas-cloud/vecdex/internal/config"
	"github.com/kailas-cloud/vecdex/internal/memorycontext"
	vecdex "github.com/kailas-cloud/vecdex/pkg/sdk"
)

const liveProtocolVersion = "2026-07-28"
const liveBearerKey = "live-integration-secret"

type liveReplica struct {
	url   string
	rt    *app.Runtime
	close func()
}

type liveConstantEmbedder struct{}

func (liveConstantEmbedder) Embed(context.Context, string) (vecdex.EmbeddingResult, error) {
	vector := make([]float32, 1024)
	vector[0] = 1
	return vecdex.EmbeddingResult{Embedding: vector}, nil
}

func TestEmbeddedSDKInvalidatesMCPAcrossReplicasLive(t *testing.T) {
	addr := os.Getenv("VECDEX_TEST_VALKEY_ADDR")
	if addr == "" {
		t.Skip("VECDEX_TEST_VALKEY_ADDR is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	owner := "mcp-live-" + uuid.NewString()
	cfg := liveRuntimeConfig(t, addr, owner)
	first := newLiveReplica(t, ctx, cfg)
	second := newLiveReplica(t, ctx, cfg)
	name := "mcp_live_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	collection := liveCallTool(t, ctx, first.url, "collection_create", map[string]any{"name": name})
	generation := collection["generation"].(string)
	opened := liveCallTool(t, ctx, first.url, "context_create", map[string]any{
		"name": "shared durable context", "collection": name, "generation": generation, "idempotency_key": "open",
	})["context"].(map[string]any)
	contextID, contextVersion := opened["context_id"].(string), opened["version"].(string)
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 20*time.Second)
		defer stop()
		_, err := second.rt.Contexts.Delete(cleanup, memorycontext.DeleteRequest{
			Owner: owner, ID: contextID, ExpectedVersion: contextVersion, IdempotencyKey: "cleanup-context",
		})
		if err != nil {
			t.Error(err)
		}
		if err := second.rt.Collections.DeleteIfGeneration(cleanup, name, generation); err != nil {
			t.Error(err)
		}
	})
	created := liveCallTool(t, ctx, first.url, "memory_create", map[string]any{
		"context_id": contextID, "id": "source", "content": "original source", "idempotency_key": "create",
	})
	updatedVersion := updateViaSDKAndObserve(t, ctx, addr, first.url, name, generation, created)
	first.close() // No transport session or process-local context may be required by the next read.
	continued := liveCallTool(t, ctx, second.url, "context_get", map[string]any{"context_id": contextID})
	if continued["version"] != contextVersion || continued["generation"] != generation || continued["stale"] != false {
		t.Fatalf("second replica lost durable context binding: %#v", continued)
	}
	current := liveCallTool(t, ctx, second.url, "memory_read", map[string]any{"context_id": contextID, "id": "source"})
	if current["version"] != updatedVersion || current["content"] != "changed by embedded SDK" {
		t.Fatalf("reconnected read did not observe the SDK commit: %#v", current)
	}
}

func liveRuntimeConfig(t *testing.T, addr, owner string) *config.Config {
	t.Helper()
	embeddings := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		vector, _ := (liveConstantEmbedder{}).Embed(context.Background(), "")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"object": "list", "model": "live-test-model",
			"data":  []map[string]any{{"object": "embedding", "index": 0, "embedding": vector.Embedding}},
			"usage": map[string]int{"prompt_tokens": 1, "total_tokens": 1},
		})
	}))
	t.Cleanup(embeddings.Close)
	cfg := &config.Config{
		Valkey: config.ValkeyConfig{Addrs: []string{addr}},
		Auth: config.AuthConfig{Tokens: []config.AuthToken{
			{PrincipalID: owner, Role: "admin", Key: liveBearerKey},
		}},
		Embedding: config.EmbeddingConfig{ActiveVectorizer: "live", Providers: map[string]config.ProviderConfig{
			"live": {Backend: "openai", APIKey: "test", BaseURL: embeddings.URL + "/v1"},
		}, Vectorizers: map[string]config.VectorizerConfig{
			"live": {Provider: "live", Model: "live-test-model", Dimensions: 1024},
		}},
	}
	cfg.ApplyDefaults()
	return cfg
}

func newLiveReplica(t *testing.T, ctx context.Context, cfg *config.Config) liveReplica {
	t.Helper()
	runtime, err := app.New(ctx, cfg, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	handler, err := New(runtime, cfg.Auth, zap.NewNop())
	if err != nil {
		runtime.Close()
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	var once sync.Once
	closeReplica := func() {
		once.Do(func() { _ = handler.Close(); server.Close(); runtime.Close() })
	}
	t.Cleanup(closeReplica)
	return liveReplica{url: server.URL + "/mcp", rt: runtime, close: closeReplica}
}

func updateViaSDKAndObserve(
	t *testing.T, ctx context.Context, addr, endpoint, name, generation string, created map[string]any,
) string {
	t.Helper()
	streamCtx, closeStream := context.WithCancel(ctx)
	defer closeStream()
	response := liveRPC(t, streamCtx, endpoint, "subscriptions/listen", map[string]any{
		"notifications": map[string]any{"resourceSubscriptions": []string{created["uri"].(string)}},
	})
	defer response.Body.Close()
	scanner := bufio.NewScanner(response.Body)
	ack := liveNextMessage(t, scanner)
	if ack["method"] != "notifications/subscriptions/acknowledged" {
		t.Fatalf("subscription was not acknowledged: %#v", ack)
	}
	client, err := vecdex.New(ctx, vecdex.WithValkey(addr, ""), vecdex.WithStandalone(),
		vecdex.WithVectorDimensions(1024), vecdex.WithEmbedder(liveConstantEmbedder{}))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	content := "changed by embedded SDK"
	updated, err := client.Documents(name).Patch(vecdex.WithCollectionGeneration(ctx, generation), "source",
		vecdex.DocumentPatch{Content: &content}, vecdex.IfVersion(created["version"].(string)),
		vecdex.IdempotencyKey("sdk-update"))
	if err != nil {
		t.Fatal(err)
	}
	notification := liveNextMessage(t, scanner)
	params, ok := notification["params"].(map[string]any)
	if !ok || notification["method"] != "notifications/resources/updated" || params["uri"] != created["uri"] {
		t.Fatalf("SDK commit did not invalidate its source resource: %#v", notification)
	}
	return updated.Version
}

func liveRPC(t *testing.T, ctx context.Context, endpoint, method string, params map[string]any) *http.Response {
	t.Helper()
	params["_meta"] = map[string]any{
		"io.modelcontextprotocol/protocolVersion":    liveProtocolVersion,
		"io.modelcontextprotocol/clientInfo":         map[string]any{"name": "vecdex-live-contract", "version": "1"},
		"io.modelcontextprotocol/clientCapabilities": map[string]any{},
	}
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+liveBearerKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Mcp-Protocol-Version", liveProtocolVersion)
	req.Header.Set("Mcp-Method", method)
	if name, ok := params["name"].(string); ok {
		req.Header.Set("Mcp-Name", name)
	}
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK {
		defer response.Body.Close()
		raw, _ := io.ReadAll(response.Body)
		t.Fatalf("%s HTTP %d: %s", method, response.StatusCode, raw)
	}
	return response
}

func liveCallTool(t *testing.T, ctx context.Context, endpoint, name string, arguments map[string]any) map[string]any {
	t.Helper()
	response := liveRPC(t, ctx, endpoint, "tools/call", map[string]any{"name": name, "arguments": arguments})
	defer response.Body.Close()
	message := liveNextMessage(t, bufio.NewScanner(response.Body))
	result, ok := message["result"].(map[string]any)
	if !ok {
		t.Fatalf("%s returned JSON-RPC error: %#v", name, message)
	}
	envelope, ok := result["structuredContent"].(map[string]any)
	if !ok || envelope["ok"] != true || result["isError"] == true {
		t.Fatalf("%s failed: %#v", name, result)
	}
	data, ok := envelope["data"].(map[string]any)
	if !ok {
		t.Fatalf("%s returned non-object data: %#v", name, envelope)
	}
	return data
}

func liveNextMessage(t *testing.T, scanner *bufio.Scanner) map[string]any {
	t.Helper()
	for scanner.Scan() {
		line := strings.TrimPrefix(scanner.Text(), "data: ")
		if !strings.HasPrefix(line, "{") {
			continue
		}
		var message map[string]any
		if err := json.Unmarshal([]byte(line), &message); err != nil {
			t.Fatal(err)
		}
		return message
	}
	t.Fatalf("MCP response stream ended before a message: %v", scanner.Err())
	return nil
}
