// Nebius Inference API embedder (копия из examples/fsqr/embedder.go).
// Дублирование минимально — loader отдельный бинарник.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	vecdex "github.com/kailas-cloud/vecdex/pkg/sdk"
)

const (
	nebiusURL   = "https://api.studio.nebius.com/v1/embeddings"
	nebiusModel = "Qwen/Qwen3-Embedding-8B"
)

type NebiusEmbedder struct {
	apiKey string
	client *http.Client
}

func NewNebiusEmbedder(apiKey string) *NebiusEmbedder {
	return &NebiusEmbedder{apiKey: apiKey, client: http.DefaultClient}
}

type embReq struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

type embResp struct {
	Data []struct {
		Embedding []float32 `json:"embedding"`
	} `json:"data"`
	Usage struct {
		PromptTokens int `json:"prompt_tokens"`
		TotalTokens  int `json:"total_tokens"`
	} `json:"usage"`
}

func (e *NebiusEmbedder) Embed(ctx context.Context, text string) (vecdex.EmbeddingResult, error) {
	body, err := json.Marshal(embReq{Model: nebiusModel, Input: []string{text}})
	if err != nil {
		return vecdex.EmbeddingResult{}, fmt.Errorf("marshal: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, nebiusURL, bytes.NewReader(body))
	if err != nil {
		return vecdex.EmbeddingResult{}, fmt.Errorf("new request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+e.apiKey)

	resp, err := e.client.Do(req)
	if err != nil {
		return vecdex.EmbeddingResult{}, fmt.Errorf("do request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return vecdex.EmbeddingResult{}, fmt.Errorf("nebius API: status %d", resp.StatusCode)
	}

	var result embResp
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return vecdex.EmbeddingResult{}, fmt.Errorf("decode: %w", err)
	}
	if len(result.Data) == 0 {
		return vecdex.EmbeddingResult{}, fmt.Errorf("nebius API: empty response")
	}

	return vecdex.EmbeddingResult{
		Embedding:    result.Data[0].Embedding,
		PromptTokens: result.Usage.PromptTokens,
		TotalTokens:  result.Usage.TotalTokens,
	}, nil
}
