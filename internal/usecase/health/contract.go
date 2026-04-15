package health

import "context"

// ValkeyPinger checks Valkey availability.
type ValkeyPinger interface {
	Ping(ctx context.Context) error
}

// EmbeddingChecker checks embedding provider availability.
type EmbeddingChecker interface {
	HealthCheck(ctx context.Context) error
}
