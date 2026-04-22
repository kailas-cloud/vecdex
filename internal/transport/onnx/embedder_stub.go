//go:build !cgo

package onnx

import (
	"context"
	"fmt"

	"github.com/kailas-cloud/vecdex/internal/domain"
)

// Embedder is a stub used when CGO is disabled.
type Embedder struct{}

// Config holds the ONNX embedding provider settings.
type Config struct {
	ModelDir          string
	Model             string
	Dimensions        int
	MaxLength         int
	ExecutionProvider string
	Provider          string
}

// NewEmbedder returns an explicit unsupported error when vecdex is built without CGO.
func NewEmbedder(_ *Config) (*Embedder, error) {
	return nil, fmt.Errorf("onnx embedding backend requires CGO_ENABLED=1")
}

// Embed implements domain.Embedder for the stub.
func (e *Embedder) Embed(_ context.Context, _ string) (domain.EmbeddingResult, error) {
	return domain.EmbeddingResult{}, fmt.Errorf("onnx embedding backend requires CGO_ENABLED=1")
}

// BatchEmbed implements domain.BatchEmbedder for the stub.
func (e *Embedder) BatchEmbed(_ context.Context, _ []string) (domain.BatchEmbeddingResult, error) {
	return domain.BatchEmbeddingResult{}, fmt.Errorf("onnx embedding backend requires CGO_ENABLED=1")
}

// HealthCheck implements domain.HealthChecker for the stub.
func (e *Embedder) HealthCheck(_ context.Context) error {
	return fmt.Errorf("onnx embedding backend requires CGO_ENABLED=1")
}
