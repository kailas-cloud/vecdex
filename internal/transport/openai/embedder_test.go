package openai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"go.uber.org/zap"

	"github.com/kailas-cloud/vecdex/internal/metrics"
)

func TestMain(m *testing.M) {
	metrics.RegisterEmbeddingMetrics()
	os.Exit(m.Run())
}

// openaiEmbeddingResponse mirrors the OpenAI-compatible API embedding response.
type openaiEmbeddingResponse struct {
	Object string `json:"object"`
	Data   []struct {
		Object    string    `json:"object"`
		Embedding []float32 `json:"embedding"`
		Index     int       `json:"index"`
	} `json:"data"`
	Model string `json:"model"`
	Usage struct {
		PromptTokens int `json:"prompt_tokens"`
		TotalTokens  int `json:"total_tokens"`
	} `json:"usage"`
}

func TestEmbedder_Embed(t *testing.T) {
	expectedVec := []float32{0.1, 0.2, 0.3, 0.4}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/embeddings" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("unexpected auth header: %s", r.Header.Get("Authorization"))
		}

		resp := openaiEmbeddingResponse{
			Object: "list",
			Model:  "test-model",
		}
		resp.Data = append(resp.Data, struct {
			Object    string    `json:"object"`
			Embedding []float32 `json:"embedding"`
			Index     int       `json:"index"`
		}{
			Object:    "embedding",
			Embedding: expectedVec,
			Index:     0,
		})
		resp.Usage.PromptTokens = 10
		resp.Usage.TotalTokens = 10

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	emb := NewEmbedder(&Config{
		APIKey:     "test-key",
		BaseURL:    server.URL,
		Model:      "test-model",
		Dimensions: 4,
		Provider:   "test",
		Logger:     zap.NewNop(),
	})

	result, err := emb.Embed(context.Background(), "hello world")
	if err != nil {
		t.Fatalf("Embed failed: %v", err)
	}

	if len(result.Embedding) != len(expectedVec) {
		t.Fatalf("expected %d dimensions, got %d", len(expectedVec), len(result.Embedding))
	}

	for i, v := range result.Embedding {
		if v != expectedVec[i] {
			t.Errorf("vec[%d] = %f, expected %f", i, v, expectedVec[i])
		}
	}
}

func TestEmbedder_EmbedReturnsUsage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := openaiEmbeddingResponse{
			Object: "list",
			Model:  "test-model",
		}
		resp.Data = append(resp.Data, struct {
			Object    string    `json:"object"`
			Embedding []float32 `json:"embedding"`
			Index     int       `json:"index"`
		}{
			Object:    "embedding",
			Embedding: []float32{0.1, 0.2},
			Index:     0,
		})
		resp.Usage.PromptTokens = 42
		resp.Usage.TotalTokens = 42

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	emb := NewEmbedder(&Config{
		APIKey:   "test-key",
		BaseURL:  server.URL,
		Model:    "test-model",
		Provider: "test",
		Logger:   zap.NewNop(),
	})

	result, err := emb.Embed(context.Background(), "hello world")
	if err != nil {
		t.Fatalf("Embed failed: %v", err)
	}

	if result.PromptTokens != 42 {
		t.Errorf("PromptTokens = %d, expected 42", result.PromptTokens)
	}
	if result.TotalTokens != 42 {
		t.Errorf("TotalTokens = %d, expected 42", result.TotalTokens)
	}
	if len(result.Embedding) != 2 {
		t.Errorf("embedding length = %d, expected 2", len(result.Embedding))
	}
}

func TestEmbedder_BatchEmbed(t *testing.T) {
	vec1 := []float32{0.1, 0.2}
	vec2 := []float32{0.3, 0.4}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Возвращаем 2 вектора в обратном порядке — проверяем сортировку по Index
		resp := openaiEmbeddingResponse{
			Object: "list",
			Model:  "test-model",
		}
		resp.Data = append(resp.Data,
			struct {
				Object    string    `json:"object"`
				Embedding []float32 `json:"embedding"`
				Index     int       `json:"index"`
			}{Object: "embedding", Embedding: vec2, Index: 1},
			struct {
				Object    string    `json:"object"`
				Embedding []float32 `json:"embedding"`
				Index     int       `json:"index"`
			}{Object: "embedding", Embedding: vec1, Index: 0},
		)
		resp.Usage.PromptTokens = 20
		resp.Usage.TotalTokens = 20

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	emb := NewEmbedder(&Config{
		APIKey:   "test-key",
		BaseURL:  server.URL,
		Model:    "test-model",
		Provider: "test",
		Logger:   zap.NewNop(),
	})

	result, err := emb.BatchEmbed(context.Background(), []string{"hello", "world"})
	if err != nil {
		t.Fatalf("BatchEmbed failed: %v", err)
	}

	if len(result.Embeddings) != 2 {
		t.Fatalf("expected 2 embeddings, got %d", len(result.Embeddings))
	}
	// Проверяем что порядок восстановлен по Index
	if result.Embeddings[0][0] != 0.1 {
		t.Errorf("expected first vec[0]=0.1, got %f", result.Embeddings[0][0])
	}
	if result.Embeddings[1][0] != 0.3 {
		t.Errorf("expected second vec[0]=0.3, got %f", result.Embeddings[1][0])
	}
	if result.TotalTokens != 20 {
		t.Errorf("expected TotalTokens=20, got %d", result.TotalTokens)
	}
}

func TestEmbedder_BatchEmbed_Empty(t *testing.T) {
	emb := NewEmbedder(&Config{
		APIKey:   "test-key",
		BaseURL:  "http://unused",
		Model:    "test-model",
		Provider: "test",
		Logger:   zap.NewNop(),
	})

	result, err := emb.BatchEmbed(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Embeddings != nil {
		t.Errorf("expected nil embeddings for empty input, got %v", result.Embeddings)
	}
}

func TestEmbedder_BatchEmbed_CountMismatch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Возвращаем 1 вектор вместо 2
		resp := openaiEmbeddingResponse{
			Object: "list",
			Model:  "test-model",
		}
		resp.Data = append(resp.Data, struct {
			Object    string    `json:"object"`
			Embedding []float32 `json:"embedding"`
			Index     int       `json:"index"`
		}{Object: "embedding", Embedding: []float32{0.1}, Index: 0})
		resp.Usage.PromptTokens = 5
		resp.Usage.TotalTokens = 5

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	emb := NewEmbedder(&Config{
		APIKey:   "test-key",
		BaseURL:  server.URL,
		Model:    "test-model",
		Provider: "test",
		Logger:   zap.NewNop(),
	})

	_, err := emb.BatchEmbed(context.Background(), []string{"a", "b"})
	if err == nil {
		t.Fatal("expected error for count mismatch")
	}
}

func TestEmbedder_APIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		json.NewEncoder(w).Encode(map[string]any{
			"error": map[string]any{
				"message": "rate limit exceeded",
				"type":    "rate_limit_error",
			},
		})
	}))
	defer server.Close()

	emb := NewEmbedder(&Config{
		APIKey:   "test-key",
		BaseURL:  server.URL,
		Model:    "test-model",
		Provider: "test",
		Logger:   zap.NewNop(),
	})

	_, err := emb.Embed(context.Background(), "hello")
	if err == nil {
		t.Fatal("expected error for 429 response")
	}
}
