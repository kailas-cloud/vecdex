package openai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	openai "github.com/sashabaranov/go-openai"
	"go.uber.org/zap"

	"github.com/kailas-cloud/vecdex/internal/domain"
	"github.com/kailas-cloud/vecdex/internal/metrics"
)

// Embedder is an embedding provider using the OpenAI-compatible API (e.g. Nebius).
type Embedder struct {
	client     *openai.Client
	model      openai.EmbeddingModel
	dimensions int
	user       string
	provider   string
	logger     *zap.Logger
}

// Config holds the embedding provider settings.
type Config struct {
	APIKey     string
	BaseURL    string
	Model      string
	Dimensions int
	User       string
	Provider   string
	Logger     *zap.Logger
}

// NewEmbedder creates an OpenAI-compatible embedding provider.
func NewEmbedder(cfg *Config) *Embedder {
	clientCfg := openai.DefaultConfig(cfg.APIKey)
	clientCfg.BaseURL = cfg.BaseURL

	return &Embedder{
		client:     openai.NewClientWithConfig(clientCfg),
		model:      openai.EmbeddingModel(cfg.Model),
		dimensions: cfg.Dimensions,
		user:       cfg.User,
		provider:   cfg.Provider,
		logger:     cfg.Logger,
	}
}

// Embed implements domain.Embedder. Returns the vector and usage with transport-level metrics.
func (e *Embedder) Embed(ctx context.Context, text string) (domain.EmbeddingResult, error) {
	req := openai.EmbeddingRequest{
		Input:          []string{text},
		Model:          e.model,
		EncodingFormat: openai.EmbeddingEncodingFormatFloat,
		User:           e.user,
	}
	if e.dimensions > 0 {
		req.Dimensions = e.dimensions
	}

	start := time.Now()

	resp, err := e.client.CreateEmbeddings(ctx, req)

	duration := time.Since(start)

	if err != nil {
		metrics.EmbeddingRequestsTotal.WithLabelValues(e.provider, string(e.model), "error").Inc()
		metrics.EmbeddingErrorsTotal.WithLabelValues(e.provider, string(e.model), "api_error").Inc()
		return domain.EmbeddingResult{}, parseAPIError(err)
	}

	if len(resp.Data) == 0 {
		metrics.EmbeddingRequestsTotal.WithLabelValues(e.provider, string(e.model), "error").Inc()
		metrics.EmbeddingErrorsTotal.WithLabelValues(e.provider, string(e.model), "empty_response").Inc()
		return domain.EmbeddingResult{}, fmt.Errorf("empty embedding response: %w", domain.ErrEmbeddingProviderError)
	}

	// Record success metrics
	metrics.EmbeddingRequestsTotal.WithLabelValues(e.provider, string(e.model), "success").Inc()
	metrics.EmbeddingRequestDuration.WithLabelValues(e.provider, string(e.model)).Observe(duration.Seconds())

	totalTokens := resp.Usage.TotalTokens
	promptTokens := resp.Usage.PromptTokens
	if totalTokens > 0 {
		metrics.EmbeddingTokensTotal.WithLabelValues(e.provider, string(e.model), "prompt").Add(float64(promptTokens))
		metrics.EmbeddingTokensTotal.WithLabelValues(e.provider, string(e.model), "total").Add(float64(totalTokens))
	}

	return domain.EmbeddingResult{
		Embedding:    resp.Data[0].Embedding,
		PromptTokens: promptTokens,
		TotalTokens:  totalTokens,
	}, nil
}

// HealthCheck verifies API availability via ListModels (free endpoint).
func (e *Embedder) HealthCheck(ctx context.Context) error {
	if _, err := e.client.ListModels(ctx); err != nil {
		return fmt.Errorf("list models: %w", err)
	}
	return nil
}

// parseAPIError extracts a human-readable error from the API response.
// All errors are wrapped with domain.ErrEmbeddingProviderError for correct 502 mapping.
func parseAPIError(err error) error {
	wrap := domain.ErrEmbeddingProviderError

	var reqErr *openai.RequestError
	if errors.As(err, &reqErr) {
		detail := extractDetail(reqErr.Body)
		if detail != "" {
			return fmt.Errorf("embedding API error %d: %s: %w",
				reqErr.HTTPStatusCode, detail, wrap)
		}
		return fmt.Errorf("embedding API error %d: %s: %w",
			reqErr.HTTPStatusCode, string(reqErr.Body), wrap)
	}

	var apiErr *openai.APIError
	if errors.As(err, &apiErr) {
		return fmt.Errorf("embedding API error %d: %s: %w",
			apiErr.HTTPStatusCode, apiErr.Message, wrap)
	}

	return fmt.Errorf("embedding request failed: %w", wrap)
}

// extractDetail extracts the "detail" field from a JSON error body (Nebius error format).
func extractDetail(body []byte) string {
	var parsed struct {
		Detail string `json:"detail"`
	}
	if json.Unmarshal(body, &parsed) == nil && parsed.Detail != "" {
		return parsed.Detail
	}
	return ""
}
