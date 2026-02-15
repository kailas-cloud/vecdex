package health

import "context"

// Status represents the aggregated health status.
type Status string

const (
	// Healthy indicates all components are operational.
	Healthy Status = "ok"
	// Degraded indicates partial failure.
	Degraded Status = "degraded"
	// Unhealthy indicates total failure.
	Unhealthy Status = "error"
)

// CheckResult represents an individual component health check outcome.
type CheckResult string

const (
	// CheckOK indicates a passing health check.
	CheckOK CheckResult = "ok"
	// CheckError indicates a failing health check.
	CheckError CheckResult = "error"
)

// Report aggregates health check results.
type Report struct {
	Status Status
	Checks map[string]CheckResult
}

// Service coordinates health checks.
type Service struct {
	db        DBPinger
	embedding EmbeddingChecker
}

// New creates a Service. embedding can be nil.
func New(db DBPinger, embedding EmbeddingChecker) *Service {
	return &Service{db: db, embedding: embedding}
}

// Check runs health checks against all components.
func (s *Service) Check(ctx context.Context) Report {
	checks := make(map[string]CheckResult)

	if err := s.db.Ping(ctx); err != nil {
		checks["database"] = CheckError
	} else {
		checks["database"] = CheckOK
	}

	if s.embedding != nil {
		if err := s.embedding.HealthCheck(ctx); err != nil {
			checks["embedding"] = CheckError
		} else {
			checks["embedding"] = CheckOK
		}
	}

	status := Healthy
	for _, v := range checks {
		if v == CheckError {
			status = Degraded
			break
		}
	}

	return Report{Status: status, Checks: checks}
}
