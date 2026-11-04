package openai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"time"

	openai "github.com/sashabaranov/go-openai"
	"go.uber.org/zap"

	"github.com/kailas-cloud/vecdex/internal/domain"
	domusage "github.com/kailas-cloud/vecdex/internal/domain/usage"
	"github.com/kailas-cloud/vecdex/internal/metrics"
)

// Embedder is an embedding provider using the OpenAI-compatible API (e.g. Nebius).
type Embedder struct {
	observer   domusage.InvocationObserver
	client     *openai.Client
	model      openai.EmbeddingModel
	dimensions int
	user       string
	provider   string
	logger     *zap.Logger
}

// Config holds the embedding provider settings.
type Config struct {
	Observer   domusage.InvocationObserver
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
	if cfg.BaseURL != "" {
		clientCfg.BaseURL = cfg.BaseURL
	}
	clientCfg.HTTPClient = &http.Client{Timeout: 30 * time.Second}

	return &Embedder{
		client:     openai.NewClientWithConfig(clientCfg),
		observer:   cfg.Observer,
		model:      openai.EmbeddingModel(cfg.Model),
		dimensions: cfg.Dimensions,
		user:       cfg.User,
		provider:   cfg.Provider,
		logger:     cfg.Logger,
	}
}

// Embed implements domain.Embedder. Returns the vector and usage with transport-level metrics.
func (e *Embedder) Embed(ctx context.Context, text string) (domain.EmbeddingResult, error) {
	result, err := e.BatchEmbed(ctx, []string{text})
	out := domain.EmbeddingResult{
		PromptTokens: result.PromptTokens,
		TotalTokens:  result.TotalTokens,
		UsageUnknown: result.UsageUnknown,
	}
	if len(result.Embeddings) > 0 {
		out.Embedding = result.Embeddings[0]
	}
	return out, err
}

// BatchEmbed implements domain.BatchEmbedder. Один вызов CreateEmbeddings с []string.
func (e *Embedder) BatchEmbed(ctx context.Context, texts []string) (domain.BatchEmbeddingResult, error) {
	if len(texts) == 0 {
		return domain.BatchEmbeddingResult{}, nil
	}

	req := openai.EmbeddingRequest{
		Input:          texts,
		Model:          e.model,
		EncodingFormat: openai.EmbeddingEncodingFormatFloat,
		User:           e.user,
	}
	if e.dimensions > 0 {
		req.Dimensions = e.dimensions
	}

	start := time.Now()

	resp, err := e.createEmbeddings(ctx, req, len(texts))

	duration := time.Since(start)

	if err != nil {
		metrics.EmbeddingRequestsTotal.WithLabelValues(e.provider, string(e.model), "error").Inc()
		metrics.EmbeddingErrorsTotal.WithLabelValues(e.provider, string(e.model), "api_error").Inc()
		return domain.BatchEmbeddingResult{}, fmt.Errorf("create embeddings: %w", err)
	}

	usage := domain.BatchEmbeddingResult{
		PromptTokens: resp.Usage.PromptTokens,
		TotalTokens:  resp.Usage.TotalTokens,
		UsageUnknown: resp.Usage.TotalTokens <= 0,
	}
	if len(resp.Data) != len(texts) {
		metrics.EmbeddingRequestsTotal.WithLabelValues(e.provider, string(e.model), "error").Inc()
		metrics.EmbeddingErrorsTotal.WithLabelValues(e.provider, string(e.model), "count_mismatch").Inc()
		return usage, fmt.Errorf(
			"embedding count mismatch: got %d, expected %d: %w",
			len(resp.Data), len(texts), domain.ErrEmbeddingProviderError,
		)
	}

	metrics.EmbeddingRequestsTotal.WithLabelValues(e.provider, string(e.model), "success").Inc()
	metrics.EmbeddingRequestDuration.WithLabelValues(e.provider, string(e.model)).Observe(duration.Seconds())

	totalTokens := resp.Usage.TotalTokens
	promptTokens := resp.Usage.PromptTokens
	if totalTokens > 0 {
		metrics.EmbeddingTokensTotal.WithLabelValues(e.provider, string(e.model), "prompt").Add(float64(promptTokens))
		metrics.EmbeddingTokensTotal.WithLabelValues(e.provider, string(e.model), "total").Add(float64(totalTokens))
	}

	// API не гарантирует порядок — выравниваем по resp.Data[i].Index
	embeddings := make([][]float32, len(texts))
	for _, d := range resp.Data {
		if d.Index < 0 || d.Index >= len(texts) || embeddings[d.Index] != nil ||
			!validVector(d.Embedding, e.dimensions) {
			return usage, fmt.Errorf(
				"invalid embedding response: %w",
				domain.ErrEmbeddingProviderError,
			)
		}
		embeddings[d.Index] = d.Embedding
	}

	return domain.BatchEmbeddingResult{
		UsageUnknown: usage.UsageUnknown,
		Embeddings:   embeddings,
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
		if code, ok := apiErr.Code.(string); ok && code == "context_length_exceeded" {
			return errors.Join(domain.ErrEmbeddingInputTooLong, domain.ErrEmbeddingProviderError)
		}
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

func validVector(v []float32, dimensions int) bool {
	if len(v) == 0 || (dimensions > 0 && len(v) != dimensions) {
		return false
	}
	for _, f := range v {
		if math.IsNaN(float64(f)) || math.IsInf(float64(f), 0) {
			return false
		}
	}
	return true
}

func (e *Embedder) createEmbeddings(
	ctx context.Context,
	req openai.EmbeddingRequest,
	inputs int,
) (openai.EmbeddingResponse, error) {
	complete, err := domusage.BeginInvocation(ctx, e.observer, inputs)
	if err != nil {
		return openai.EmbeddingResponse{}, fmt.Errorf("openai accounting: %w", err)
	}
	resp, err := e.client.CreateEmbeddings(ctx, req)
	unknown := resp.Usage.TotalTokens <= 0
	if err != nil {
		var apiErr *openai.APIError
		var requestErr *openai.RequestError
		if errors.As(err, &apiErr) && apiErr.HTTPStatusCode >= 400 && apiErr.HTTPStatusCode < 500 {
			unknown = false
		}
		if errors.As(err, &requestErr) && requestErr.HTTPStatusCode >= 400 && requestErr.HTTPStatusCode < 500 {
			unknown = false
		}
	}
	complete(resp.Usage.TotalTokens, unknown)
	if err != nil {
		return resp, parseAPIError(err)
	}
	return resp, nil
}
