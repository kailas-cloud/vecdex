package vecdex

import "context"

// Embedder converts text to vector embeddings.
// Required for text collections; geo collections work without it.
type Embedder interface {
	Embed(ctx context.Context, text string) (EmbeddingResult, error)
}

// EmbeddingResult carries the embedding vector and token counts.
type EmbeddingResult struct {
	Embedding    []float32
	PromptTokens int
	TotalTokens  int
}
