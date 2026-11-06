// Example using Vecdex through the official MCP Go client.
// Creates a temporary collection and durable context, searches 12 memories,
// then deletes both using their exact versions/generations.
// Run with an admin token: VECDEX_API_KEY=... go run ./examples/months/
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type month struct {
	id      string
	content string
	season  string
}

var months = []month{
	{
		"january",
		"January is the first month of the year. It is cold and snowy in the northern hemisphere.",
		"winter",
	},
	{
		"february",
		"February is the shortest month. Winter is still strong, with freezing temperatures.",
		"winter",
	},
	{
		"march",
		"March marks the beginning of spring. Snow melts, days grow longer, and nature awakens.",
		"spring",
	},
	{
		"april",
		"April brings warm rains and blooming flowers. Spring is in full swing with green fields.",
		"spring",
	},
	{
		"may",
		"May is late spring. Trees are fully green, temperatures are pleasant.",
		"spring",
	},
	{
		"june",
		"June is the start of summer. Long sunny days, warm weather, and vacations begin.",
		"summer",
	},
	{
		"july",
		"July is the hottest month of summer. Beach trips and outdoor adventures everywhere.",
		"summer",
	},
	{
		"august",
		"August is late summer. The heat persists but evenings start to cool.",
		"summer",
	},
	{
		"september",
		"September marks the beginning of autumn. Leaves change color and temperatures drop.",
		"autumn",
	},
	{
		"october",
		"October is the heart of autumn. Forests turn golden and red, the air becomes crisp.",
		"autumn",
	},
	{
		"november",
		"November is late autumn approaching winter. Trees are bare, frost appears.",
		"autumn",
	},
	{
		"december",
		"December is the last month. Winter arrives with snow, holidays, and celebrations.",
		"winter",
	},
}

type collectionInfo struct {
	Generation string `json:"generation"`
}

type contextInfo struct {
	ID      string `json:"context_id"`
	Version string `json:"version"`
}

type contextReceipt struct {
	Context contextInfo `json:"context"`
}

type searchResults struct {
	Items []struct {
		ID      string  `json:"id"`
		Score   float64 `json:"score"`
		Content string  `json:"content"`
	} `json:"items"`
}

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	session, err := connect(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = session.Close() }()

	name := "months-" + uuid.NewString()
	collection, err := callTool[collectionInfo](ctx, session, "collection_create", map[string]any{
		"name":   name,
		"fields": []map[string]string{{"name": "season", "type": "tag"}},
	})
	if err != nil {
		return err
	}
	defer cleanupCollection(session, name, collection.Generation)

	receipt, err := callTool[contextReceipt](ctx, session, "context_create", map[string]any{
		"name": "Months example", "collection": name, "generation": collection.Generation,
		"idempotency_key": "months-context-" + name,
	})
	if err != nil {
		return err
	}
	defer cleanupContext(session, receipt.Context)
	if err := createMonths(ctx, session, receipt.Context.ID); err != nil {
		return err
	}

	// Writes are durable before Search has necessarily indexed them. This probe
	// waits for index readiness; it is separate from the example queries.
	if err := waitForSearch(ctx, session, receipt.Context.ID); err != nil {
		return err
	}
	for _, query := range []string{"winter", "hot sunny beach", "cold"} {
		args := map[string]any{"context_id": receipt.Context.ID, "query": query, "limit": 5}
		if query == "cold" {
			args["filters"] = map[string]any{"must": []map[string]string{{"key": "season", "match": "winter"}}}
		}
		results, err := callTool[searchResults](ctx, session, "memory_search", args)
		if err != nil {
			return err
		}
		fmt.Printf("\nSearch %q:\n", query)
		for _, hit := range results.Items {
			fmt.Printf("  %-10s score=%.4f %s\n", hit.ID, hit.Score, hit.Content)
		}
	}
	return nil
}

func connect(ctx context.Context) (*mcp.ClientSession, error) {
	endpoint := os.Getenv("VECDEX_MCP_URL")
	if endpoint == "" {
		endpoint = "http://localhost:8080/mcp"
	}
	key := os.Getenv("VECDEX_API_KEY")
	if key == "" {
		return nil, fmt.Errorf("VECDEX_API_KEY must contain an admin bearer token")
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "vecdex-months", Version: "1.0.0"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint: endpoint, HTTPClient: &http.Client{Transport: bearerTransport{key: key}, Timeout: time.Minute},
		DisableStandaloneSSE: true, MaxRetries: -1,
	}, nil)
	if err != nil {
		return nil, fmt.Errorf("connect MCP: %w", err)
	}
	return session, nil
}

type bearerTransport struct{ key string }

func (t bearerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	copyReq := req.Clone(req.Context())
	copyReq.Header.Set("Authorization", "Bearer "+t.key)
	response, err := http.DefaultTransport.RoundTrip(copyReq)
	if err != nil {
		return nil, fmt.Errorf("MCP request: %w", err)
	}
	return response, nil
}

func callTool[T any](ctx context.Context, session *mcp.ClientSession, name string, args map[string]any) (T, error) {
	var zero T
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		return zero, fmt.Errorf("%s: %w", name, err)
	}
	raw, err := json.Marshal(result.StructuredContent)
	if err != nil {
		return zero, fmt.Errorf("encode %s result: %w", name, err)
	}
	var envelope struct {
		OK    bool            `json:"ok"`
		Data  T               `json:"data"`
		Error json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return zero, fmt.Errorf("decode %s: %w", name, err)
	}
	if result.IsError || !envelope.OK {
		return zero, fmt.Errorf("%s failed: %s", name, envelope.Error)
	}
	return envelope.Data, nil
}

func createMonths(ctx context.Context, session *mcp.ClientSession, contextID string) error {
	for _, m := range months {
		_, err := callTool[map[string]any](ctx, session, "memory_create", map[string]any{
			"context_id": contextID, "id": m.id, "content": m.content,
			"metadata": map[string]string{"season": m.season}, "idempotency_key": "months-create-" + m.id,
		})
		if err != nil {
			return err
		}
		fmt.Printf("Added %s (%s)\n", m.id, m.season)
	}
	return nil
}

func waitForSearch(ctx context.Context, session *mcp.ClientSession, contextID string) error {
	for range 20 {
		result, err := callTool[searchResults](ctx, session, "memory_search", map[string]any{
			"context_id": contextID, "query": "January", "mode": "keyword", "limit": 1,
		})
		if err != nil {
			return err
		}
		if len(result.Items) > 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("wait for Search indexing: %w", ctx.Err())
		case <-time.After(500 * time.Millisecond):
		}
	}
	return fmt.Errorf("search did not index the example memories in time")
}

func cleanupContext(session *mcp.ClientSession, c contextInfo) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, err := callTool[map[string]any](ctx, session, "context_delete", map[string]any{
		"context_id": c.ID, "expected_version": c.Version, "idempotency_key": "months-delete-context-" + c.ID,
	})
	if err != nil {
		log.Printf("Context cleanup: %v", err)
	}
}

func cleanupCollection(session *mcp.ClientSession, name, generation string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, err := callTool[map[string]any](ctx, session, "collection_delete", map[string]any{
		"name": name, "generation": generation, "confirm_name": name,
	})
	if err != nil {
		log.Printf("Collection cleanup: %v", err)
	}
}
