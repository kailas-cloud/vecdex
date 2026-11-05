package vecdex

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/kailas-cloud/vecdex/internal/domain"
	"github.com/kailas-cloud/vecdex/internal/domain/storage"
)

type integrationEmbedder struct{}

func (integrationEmbedder) Embed(_ context.Context, text string) (EmbeddingResult, error) {
	if len(text) > 512 {
		return EmbeddingResult{}, domain.ErrEmbeddingInputTooLong
	}
	return EmbeddingResult{
		Embedding:    vectorFromText(text),
		PromptTokens: 1,
		TotalTokens:  1,
	}, nil
}

func (integrationEmbedder) BatchEmbed(ctx context.Context, texts []string) (BatchEmbeddingResult, error) {
	embeddings := make([][]float32, len(texts))
	for i, text := range texts {
		res, err := integrationEmbedder{}.Embed(ctx, text)
		if err != nil {
			return BatchEmbeddingResult{}, err
		}
		embeddings[i] = res.Embedding
	}
	return BatchEmbeddingResult{
		Embeddings:   embeddings,
		PromptTokens: len(texts),
		TotalTokens:  len(texts),
	}, nil
}

func vectorFromText(text string) []float32 {
	if strings.Contains(strings.ToLower(text), "scifact") {
		return []float32{1, 0, 0}
	}
	return []float32{0, 1, 0}
}

func TestTextIngestorDocumentLifecycleIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}

	valkeyAddr := resolveIntegrationValkeyAddr()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	client, err := newIntegrationClient(ctx, valkeyAddr)
	if err != nil {
		t.Skipf("integration Valkey unavailable: %v", err)
	}
	defer client.Close()

	collectionName := createIntegrationCollection(t, ctx, client)
	defer client.Collections().Delete(context.Background(), collectionName)

	ingestor := NewTextIngestor(client.Documents(collectionName), &IngestorConfig{BatchSize: 100, Parallelism: 4})

	longText := strings.TrimSpace(strings.Repeat(
		"SciFact benchmark sentence about evidence retrieval and chunked ingest performance. ",
		80,
	))
	resp, err := ingestor.Upsert(ctx, []BatchUpsertItem{{
		ID:      "paper-scifact",
		Content: longText,
	}})
	if err != nil {
		t.Fatalf("ingestor.Upsert() error = %v", err)
	}
	if resp.Succeeded != 1 {
		t.Fatalf("Succeeded = %d, want one source document", resp.Succeeded)
	}

	searchResp := waitForDocumentSearch(t, ctx, client, collectionName)
	assertDocumentSearchResults(t, searchResp.Results)

	doc, err := client.Documents(collectionName).Get(ctx, "paper-scifact")
	if err != nil || doc.Content != longText {
		t.Fatalf("read source: err=%v", err)
	}
	verifyFragmentStorageAndMetadataPatch(t, ctx, client, collectionName, longText)
	if _, err := ingestor.Upsert(ctx, []BatchUpsertItem{{ID: "paper-scifact", Content: "short replacement"}}); err != nil {
		t.Fatal(err)
	}
	if count, err := client.Documents(collectionName).Count(ctx); err != nil || count != 1 {
		t.Fatalf("count=%d err=%v", count, err)
	}
	if err := client.Documents(collectionName).Delete(ctx, "paper-scifact"); err != nil {
		t.Fatal(err)
	}
	if count, err := client.Documents(collectionName).Count(ctx); err != nil || count != 0 {
		t.Fatalf("count after delete=%d err=%v", count, err)
	}

}

func resolveIntegrationValkeyAddr() string {
	valkeyAddr := os.Getenv("VECDEX_E2E_VALKEY_ADDR")
	if valkeyAddr == "" {
		valkeyAddr = os.Getenv("VALKEY_ADDR")
	}
	if valkeyAddr == "" {
		valkeyAddr = "localhost:6379"
	}
	return valkeyAddr
}

func newIntegrationClient(ctx context.Context, valkeyAddr string) (*Client, error) {
	opts := []Option{WithEmbedder(integrationEmbedder{}), WithVectorDimensions(3)}
	if seeds := os.Getenv("VECDEX_E2E_VALKEY_CLUSTER_ADDRS"); seeds != "" {
		opts = append(opts, WithValkeyAddresses(strings.Split(seeds, ","), ""))
	} else {
		opts = append(opts, WithValkey(valkeyAddr, ""), WithStandalone())
	}
	return New(ctx, opts...)
}

func createIntegrationCollection(t *testing.T, ctx context.Context, client *Client) string {
	t.Helper()
	collectionName := fmt.Sprintf("sdk_ingest_%d", time.Now().UnixNano())
	if _, err := client.Collections().Ensure(ctx, collectionName); err != nil {
		t.Fatalf("ensure collection: %v", err)
	}
	return collectionName
}

func waitForDocumentSearch(
	t *testing.T,
	ctx context.Context,
	client *Client,
	collectionName string,
) SearchResponse {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		resp, err := client.Search(collectionName).Query(ctx, "SciFact benchmark", &SearchOptions{
			Mode:  ModeKeyword,
			Limit: 20,
		})
		if err == nil && len(resp.Results) >= 1 {
			return resp
		}
		if time.Now().After(deadline) {
			t.Fatalf("document search did not converge, err=%v results=%d", err, len(resp.Results))
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func assertDocumentSearchResults(t *testing.T, results []SearchResult) {
	t.Helper()
	if len(results) != 1 {
		t.Fatalf("len(results) = %d, want 1", len(results))
	}
	for _, item := range results {
		if item.ID != "paper-scifact" {
			t.Fatalf("id = %q, want paper-scifact", item.ID)
		}
	}
}

func verifyFragmentStorageAndMetadataPatch(
	t *testing.T, ctx context.Context, client *Client, collectionName, longText string,
) {
	t.Helper()
	col, err := client.collSvc.Get(ctx, collectionName)
	if err != nil {
		t.Fatal(err)
	}
	layout, err := storage.NewCollection(collectionName, col.Generation())
	if err != nil {
		t.Fatal(err)
	}
	parent, err := client.store.HGetAll(ctx, layout.Document("paper-scifact"))
	if err != nil {
		t.Fatal(err)
	}
	if parent["__vector"] != "" || strings.Contains(parent["__parts"], "vector") {
		t.Fatal("parent duplicates fragment vectors")
	}
	value := "metadata-only change"
	update := map[string]any{"note": value}
	patched, err := client.Documents(collectionName).
		Patch(ctx, "paper-scifact", DocumentPatch{Metadata: &update})
	fetched, fetchErr := client.Documents(collectionName).Get(ctx, "paper-scifact")
	if err != nil || fetchErr != nil || fetched.Version != patched.Version ||
		fetched.Content != longText || fetched.Metadata["note"] != value {
		t.Fatalf("metadata patch did not preserve source fragments: %v", err)
	}

}
