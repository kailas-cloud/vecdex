package vecdex

import (
	"context"
	"net/http"
	"net/url"
	"time"

	domusage "github.com/kailas-cloud/vecdex/internal/domain/usage"
	"github.com/kailas-cloud/vecdex/internal/domain/usage/budget"
	"github.com/kailas-cloud/vecdex/internal/domain/usage/metrics"
	gen "github.com/kailas-cloud/vecdex/internal/transport/generated"
	healthuc "github.com/kailas-cloud/vecdex/internal/usecase/health"
)

type httpHealth struct{ *httpBackend }
type httpUsage struct{ *httpBackend }

func (s *httpHealth) Check(ctx context.Context) healthuc.Report {
	var reply gen.HealthResponse
	if _, err := s.call(ctx, http.MethodGet, "/health", nil, &reply, http.StatusServiceUnavailable); err != nil {
		return healthuc.Report{Status: healthuc.Unhealthy,
			Checks: map[string]healthuc.CheckResult{"http": healthuc.CheckError}}
	}
	checks := make(map[string]healthuc.CheckResult, len(reply.Checks))
	for name, value := range reply.Checks {
		checks[name] = healthuc.CheckResult(value)
	}
	return healthuc.Report{Status: healthuc.Status(reply.Status), Checks: checks}
}

func (s *httpUsage) GetReport(ctx context.Context, period domusage.Period) (domusage.Report, error) {
	var reply gen.UsageResponse
	path := "/api/v2/usage?period=" + url.QueryEscape(string(period))
	if _, err := s.call(ctx, http.MethodGet, path, nil, &reply); err != nil {
		return domusage.Report{}, err
	}
	cost := 0
	if reply.Usage.CostMillidollars != nil {
		cost = *reply.Usage.CostMillidollars
	}
	measured := metrics.New(reply.Usage.EmbeddingRequests, reply.Usage.Tokens, cost).
		WithUnknownRequests(reply.Usage.UnknownRequests)
	quota := budget.New(reply.Budget.TokensLimit, reply.Budget.TokensRemaining,
		reply.Budget.IsExhausted, httpUnixMillis(reply.Budget.ResetsAt))
	return domusage.NewReport(domusage.Period(reply.Period), httpUnixMillis(reply.PeriodStartAt),
		httpUnixMillis(reply.PeriodEndAt), "", measured, quota), nil
}

func httpUnixMillis(value *time.Time) int64 {
	if value == nil {
		return 0
	}
	return value.UnixMilli()
}
