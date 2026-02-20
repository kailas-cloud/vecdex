package vecdex

import "context"

// Embedder converts text to vector embeddings.
// Required for text collections; geo collections work without it.
type Embedder interface {
	Embed(ctx context.Context, text string) (EmbeddingResult, error)
}

// BatchEmbedder vectorizes multiple texts in a single API call.
// Optional — if the provided Embedder also implements BatchEmbedder,
// batch operations will use it for significantly better throughput.
type BatchEmbedder interface {
	BatchEmbed(ctx context.Context, texts []string) (BatchEmbeddingResult, error)
}

// EmbeddingResult carries the embedding vector and token counts.
type EmbeddingResult struct {
	Embedding    []float32
	PromptTokens int
	TotalTokens  int
}

// BatchEmbeddingResult carries multiple embedding vectors and aggregate token usage.
type BatchEmbeddingResult struct {
	Embeddings   [][]float32
	PromptTokens int
	TotalTokens  int
}
