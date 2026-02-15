package domain

// VectorConfig holds internal vectorization settings, not exposed to clients.
type VectorConfig struct {
	Model               string
	Dimensions          int
	ContextWindowTokens int
	DistanceMetric      string
	Algorithm           string
	DocumentInstruction string
	QueryInstruction    string
	MaxDocumentSizeKB   int
}

// DefaultVectorConfig returns the default configuration tuned for Qwen3-Embedding-8B.
func DefaultVectorConfig() VectorConfig {
	return VectorConfig{
		Model:               "Qwen3-Embedding-8B",
		Dimensions:          1024,
		ContextWindowTokens: 41000,
		DistanceMetric:      "cosine",
		Algorithm:           "hnsw",
		DocumentInstruction: "Represent this document for semantic search",
		QueryInstruction:    "Represent this query for retrieving similar documents",
		MaxDocumentSizeKB:   164,
	}
}
