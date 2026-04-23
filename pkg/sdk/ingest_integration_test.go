package vecdex

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type integrationEmbedder struct{}

func (integrationEmbedder) Embed(_ context.Context, text string) (EmbeddingResult, error) {
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

func TestTextIngestorChunkingAndDocumentSearchIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}

	valkeyAddr := resolveIntegrationValkeyAddr()
	modelDir := resolveIntegrationModelDir()
	if _, err := os.Stat(modelDir); err != nil {
		t.Skipf("integration model dir unavailable: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	client, err := newIntegrationClient(ctx, valkeyAddr)
	if err != nil {
		t.Skipf("integration Valkey unavailable: %v", err)
	}
	defer client.Close()

	collectionName := createIntegrationCollection(t, ctx, client)
	defer client.Collections().Delete(context.Background(), collectionName)

	ingestor := newIntegrationIngestor(t, client, collectionName, modelDir)

	longText := strings.TrimSpace(strings.Repeat(
		"SciFact benchmark sentence about evidence retrieval and chunked ingest performance. ",
		80,
	))
	resp, err := ingestor.Upsert(ctx, []Document{{
		ID:      "paper-scifact",
		Content: longText,
	}})
	if err != nil {
		t.Fatalf("ingestor.Upsert() error = %v", err)
	}
	if resp.Succeeded < 2 {
		t.Fatalf("Succeeded = %d, want at least 2 chunks", resp.Succeeded)
	}

	searchResp := waitForDocumentSearch(t, ctx, client, collectionName)
	assertDocumentSearchResults(t, searchResp.Results)

	rangeResp, err := queryChunkRange(ctx, client, collectionName)
	if err != nil {
		t.Fatalf("range search: %v", err)
	}
	assertChunkRangeResults(t, rangeResp.Results)
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

func resolveIntegrationModelDir() string {
	modelDir := os.Getenv("VECDEX_E2E_MODEL_DIR")
	if modelDir == "" {
		modelDir = filepath.Clean(filepath.Join("..", "..", "models", "all-MiniLM-L6-v2"))
	}
	return modelDir
}

func newIntegrationClient(ctx context.Context, valkeyAddr string) (*Client, error) {
	return New(
		ctx,
		WithValkey(valkeyAddr, ""),
		WithStandalone(),
		WithEmbedder(integrationEmbedder{}),
		WithVectorDimensions(3),
	)
}

func createIntegrationCollection(t *testing.T, ctx context.Context, client *Client) string {
	t.Helper()
	collectionName := fmt.Sprintf("sdk_ingest_%d", time.Now().UnixNano())
	if _, err := client.Collections().Ensure(ctx, collectionName); err != nil {
		t.Fatalf("ensure collection: %v", err)
	}
	return collectionName
}

func newIntegrationIngestor(
	t *testing.T,
	client *Client,
	collectionName string,
	modelDir string,
) *TextIngestor {
	t.Helper()
	chunker, err := NewONNXChunker(ONNXChunkerConfig{
		ModelDir:       modelDir,
		WindowTokens:   DefaultChunkWindowTokens,
		OverlapPercent: DefaultChunkOverlapPercent,
	})
	if err != nil {
		t.Fatalf("NewONNXChunker() error = %v", err)
	}
	return NewTextIngestor(
		client.Documents(collectionName),
		chunker,
		&IngestorConfig{BatchSize: 100, Parallelism: 4},
	)
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
			TopK:  20,
			Limit: 20,
			Filters: FilterExpression{
				Must: []FilterCondition{{Key: ParentDocIDTag, Match: "paper-scifact"}},
			},
		})
		if err == nil && len(resp.Results) >= 2 {
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
	for _, item := range results {
		if item.Tags[ParentDocIDTag] != "paper-scifact" {
			t.Fatalf("parent_doc_id = %q, want paper-scifact", item.Tags[ParentDocIDTag])
		}
		if item.Numerics[ChunkIndexNumeric] < 1 {
			t.Fatalf("chunk_index = %v, want >= 1", item.Numerics[ChunkIndexNumeric])
		}
	}
}

func queryChunkRange(
	ctx context.Context,
	client *Client,
	collectionName string,
) (SearchResponse, error) {
	return client.Search(collectionName).Query(ctx, "SciFact benchmark", &SearchOptions{
		Mode:  ModeKeyword,
		TopK:  20,
		Limit: 20,
		Filters: FilterExpression{
			Must: []FilterCondition{
				{Key: ParentDocIDTag, Match: "paper-scifact"},
				{Key: ChunkIndexNumeric, Range: &RangeFilter{GTE: ptrFloat64(2)}},
			},
		},
	})
}

func assertChunkRangeResults(t *testing.T, results []SearchResult) {
	t.Helper()
	if len(results) == 0 {
		t.Fatal("expected chunk_index>=2 results")
	}
	for _, item := range results {
		if item.Numerics[ChunkIndexNumeric] < 2 {
			t.Fatalf("chunk_index range filter violated: %v", item.Numerics[ChunkIndexNumeric])
		}
	}
}

func ptrFloat64(v float64) *float64 {
	return &v
}
