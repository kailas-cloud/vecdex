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
	res, err := e.call(ctx, []string{text})
	if err != nil {
		return vecdex.EmbeddingResult{}, err
	}
	return vecdex.EmbeddingResult{
		Embedding:    res.Data[0].Embedding,
		PromptTokens: res.Usage.PromptTokens,
		TotalTokens:  res.Usage.TotalTokens,
	}, nil
}

// BatchEmbed отправляет несколько текстов в один API вызов.
func (e *NebiusEmbedder) BatchEmbed(ctx context.Context, texts []string) (vecdex.BatchEmbeddingResult, error) {
	res, err := e.call(ctx, texts)
	if err != nil {
		return vecdex.BatchEmbeddingResult{}, err
	}
	if len(res.Data) != len(texts) {
		return vecdex.BatchEmbeddingResult{}, fmt.Errorf(
			"nebius API: got %d embeddings, expected %d", len(res.Data), len(texts))
	}
	embeddings := make([][]float32, len(res.Data))
	for i, d := range res.Data {
		embeddings[i] = d.Embedding
	}
	return vecdex.BatchEmbeddingResult{
		Embeddings:   embeddings,
		PromptTokens: res.Usage.PromptTokens,
		TotalTokens:  res.Usage.TotalTokens,
	}, nil
}

func (e *NebiusEmbedder) call(ctx context.Context, input []string) (*embResp, error) {
	body, err := json.Marshal(embReq{Model: nebiusModel, Input: input})
	if err != nil {
		return nil, fmt.Errorf("marshal: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, nebiusURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("new request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+e.apiKey)

	resp, err := e.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("do request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("nebius API: status %d", resp.StatusCode)
	}

	var result embResp
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	if len(result.Data) == 0 {
		return nil, fmt.Errorf("nebius API: empty response")
	}
	return &result, nil
}
