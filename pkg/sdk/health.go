package vecdex

import (
	"context"

	healthuc "github.com/kailas-cloud/vecdex/internal/usecase/health"
)

// HealthStatus represents the aggregated system health.
type HealthStatus struct {
	Status string            // "ok", "degraded", "error"
	Checks map[string]string // component → "ok"/"error"
}

// Health checks the health of all system components.
func (c *Client) Health(ctx context.Context) HealthStatus {
	report := c.healthSvc.Check(ctx)
	checks := make(map[string]string, len(report.Checks))
	for k, v := range report.Checks {
		checks[k] = string(v)
	}
	return HealthStatus{
		Status: string(report.Status),
		Checks: checks,
	}
}

// healthUseCase is the internal interface for health checks.
type healthUseCase interface {
	Check(ctx context.Context) healthuc.Report
}
