package health

import "context"

// DBPinger checks database availability.
type DBPinger interface {
	Ping(ctx context.Context) error
}

// EmbeddingChecker checks embedding provider availability.
type EmbeddingChecker interface {
	HealthCheck(ctx context.Context) error
}
