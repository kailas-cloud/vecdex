package health

import "context"

// ValkeyChecker checks primary coverage and Search readiness.
type ValkeyChecker interface {
	Ready(ctx context.Context) error
}

// EmbeddingChecker checks embedding provider availability.
type EmbeddingChecker interface {
	HealthCheck(ctx context.Context) error
}
