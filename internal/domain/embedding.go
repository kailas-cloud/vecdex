package domain

import (
	"context"
	"fmt"
)

// Embedder is the shared text vectorization contract between layers.
type Embedder interface {
	Embed(ctx context.Context, text string) (EmbeddingResult, error)
}

// HealthChecker verifies embedding provider availability.
type HealthChecker interface {
	HealthCheck(ctx context.Context) error
}

// EmbeddingResult carries the embedding vector and token usage through the decorator chain.
type EmbeddingResult struct {
	Embedding    []float32
	PromptTokens int
	TotalTokens  int
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
		return EmbeddingResult{}, fmt.Errorf("instruction embed: %w", err)
	}
	return result, nil
}
