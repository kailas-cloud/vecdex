package domain

import (
	"context"
	"fmt"
	"math"
)

// Embedder is the shared text vectorization contract between layers.
type Embedder interface {
	Embed(ctx context.Context, text string) (EmbeddingResult, error)
}

// BatchEmbedder vectorizes multiple texts in a single API call.
type BatchEmbedder interface {
	BatchEmbed(ctx context.Context, texts []string) (BatchEmbeddingResult, error)
}

// HealthChecker verifies embedding provider availability.
type HealthChecker interface {
	HealthCheck(ctx context.Context) error
}

// EmbeddingResult carries the embedding vector and token usage through the decorator chain.
type EmbeddingResult struct {
	UsageUnknown bool
	Embedding    []float32
	PromptTokens int
	TotalTokens  int
}

// BatchEmbeddingResult carries multiple embedding vectors and aggregate token usage.
type BatchEmbeddingResult struct {
	UsageUnknown bool
	Embeddings   [][]float32
	PromptTokens int
	TotalTokens  int
}

// BatchFallback вызывает Embed по одному для каждого текста. Safety net для провайдеров
// без нативного batch.
func BatchFallback(ctx context.Context, e Embedder, texts []string) (BatchEmbeddingResult, error) {
	embeddings := make([][]float32, len(texts))
	var totalPrompt, totalTokens int
	var unknown bool

	for i, text := range texts {
		res, err := e.Embed(ctx, text)
		totalPrompt += res.PromptTokens
		totalTokens += res.TotalTokens
		unknown = unknown || res.UsageUnknown
		if err != nil {
			return BatchEmbeddingResult{
				Embeddings:   embeddings[:i],
				PromptTokens: totalPrompt,
				TotalTokens:  totalTokens,
				UsageUnknown: unknown,
			}, fmt.Errorf(
				"fallback embed [%d]: %w",
				i,
				err,
			)
		}
		embeddings[i] = res.Embedding
	}

	return BatchEmbeddingResult{
		UsageUnknown: unknown,
		Embeddings:   embeddings,
		PromptTokens: totalPrompt,
		TotalTokens:  totalTokens,
	}, nil
}

// InstructionEmbedder is a domain decorator that prepends instruction text before embedding.
type InstructionEmbedder struct {
	inner       Embedder
	instruction string
}

// NewInstructionEmbedder creates a decorator that prepends instruction text.
func NewInstructionEmbedder(inner Embedder, instruction string) *InstructionEmbedder {
	return &InstructionEmbedder{inner: inner, instruction: instruction}
}

// Embed prepends instruction and delegates to inner embedder.
func (e *InstructionEmbedder) Embed(ctx context.Context, text string) (EmbeddingResult, error) {
	result, err := e.inner.Embed(ctx, e.instruction+text)
	if err != nil {
		return result, fmt.Errorf("instruction embed: %w", err)
	}
	return result, nil
}

// BatchEmbed prepends instruction to each text and delegates to inner BatchEmbedder.
// Если inner не поддерживает batch — fallback на поштучный Embed.
func (e *InstructionEmbedder) BatchEmbed(ctx context.Context, texts []string) (BatchEmbeddingResult, error) {
	prefixed := make([]string, len(texts))
	for i, t := range texts {
		prefixed[i] = e.instruction + t
	}

	if be, ok := e.inner.(BatchEmbedder); ok {
		res, err := be.BatchEmbed(ctx, prefixed)
		if err != nil {
			return res, fmt.Errorf("instruction batch embed: %w", err)
		}
		return res, nil
	}

	res, err := BatchFallback(ctx, e.inner, prefixed)
	if err != nil {
		return res, fmt.Errorf("instruction batch embed fallback: %w", err)
	}
	return res, nil
}

// HealthCheck forwards provider availability checks.
func (e *InstructionEmbedder) HealthCheck(ctx context.Context) error {
	if h, ok := e.inner.(HealthChecker); ok {
		if err := h.HealthCheck(ctx); err != nil {
			return fmt.Errorf("embedding health: %w", err)
		}
	}
	return nil
}

// ValidateVector rejects incomplete, mismatched and non-finite embeddings before storage.
func ValidateVector(vector []float32, dimensions int) error {
	if len(vector) == 0 || (dimensions > 0 && len(vector) != dimensions) {
		return fmt.Errorf("got %d vector dimensions, expected %d: %w", len(vector), dimensions, ErrVectorDimMismatch)
	}
	for _, value := range vector {
		if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
			return fmt.Errorf("non-finite vector: %w", ErrEmbeddingProviderError)
		}
	}
	return nil
}
