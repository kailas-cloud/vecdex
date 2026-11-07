package server

import (
	"context"
	"sync"

	"github.com/go-chi/chi/v5/middleware"
	"github.com/prometheus/client_golang/prometheus"
	"go.uber.org/zap"

	transport "github.com/kailas-cloud/vecdex/internal/transport/mcp"
)

var (
	mcpMetricsOnce sync.Once
	mcpRequests    = mcpCounter("requests_total", "MCP operations by method, tool and stable outcome.",
		"method", "tool", "outcome")
	mcpDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name: "vecdex_mcp_request_duration_seconds",
		Help: "MCP operation latency, including subscription stream lifetime.",
	}, []string{"method", "tool"})
	mcpBytes = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name: "vecdex_mcp_response_bytes", Help: "Encoded MCP result bytes before SDK envelope enrichment.",
		Buckets: []float64{1024, 4096, 8192, 16384, 32768, 65536, 131072, 262144},
	}, []string{"method", "tool"})
	mcpConflicts     = mcpCounter("conflicts_total", "Rejected conditional or idempotency conflicts.", "tool")
	mcpReplays       = mcpCounter("replays_total", "Verified durable receipt replays.", "tool")
	mcpTruncation    = mcpCounter("truncated_results_total", "Explicitly truncated application results.", "tool")
	mcpSubscriptions = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "vecdex_mcp_active_subscriptions", Help: "Active subscription listener streams.",
	})
	mcpPollErrors = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "vecdex_mcp_subscription_poll_errors_total", Help: "Dependency failures during subscription polling.",
	})
)

func mcpCounter(name, help string, labels ...string) *prometheus.CounterVec {
	return prometheus.NewCounterVec(prometheus.CounterOpts{Name: "vecdex_mcp_" + name, Help: help}, labels)
}

func observeMCP(logger *zap.Logger) func(context.Context, transport.Event) {
	mcpMetricsOnce.Do(func() {
		prometheus.MustRegister(mcpRequests, mcpDuration, mcpBytes, mcpConflicts, mcpReplays,
			mcpTruncation, mcpSubscriptions, mcpPollErrors)
	})
	return func(ctx context.Context, e transport.Event) {
		if e.SubscriptionDelta != 0 {
			mcpSubscriptions.Add(float64(e.SubscriptionDelta))
		}
		if e.PollError {
			mcpPollErrors.Inc()
		}
		if e.Method == "" || e.Outcome == "" {
			return
		}
		mcpRequests.WithLabelValues(e.Method, e.Tool, e.Outcome).Inc()
		mcpDuration.WithLabelValues(e.Method, e.Tool).Observe(e.Duration.Seconds())
		mcpBytes.WithLabelValues(e.Method, e.Tool).Observe(float64(e.ResponseBytes))
		if e.Conflict {
			mcpConflicts.WithLabelValues(e.Tool).Inc()
		}
		if e.Replayed {
			mcpReplays.WithLabelValues(e.Tool).Inc()
		}
		if e.Truncated {
			mcpTruncation.WithLabelValues(e.Tool).Inc()
		}
		logger.Info("mcp_request", zap.String("request_id", middleware.GetReqID(ctx)),
			zap.String("method", e.Method), zap.String("tool", e.Tool), zap.String("outcome", e.Outcome),
			zap.Duration("latency", e.Duration), zap.Int("response_bytes", e.ResponseBytes),
			zap.Bool("conflict", e.Conflict), zap.Bool("replayed", e.Replayed), zap.Bool("truncated", e.Truncated))
	}
}
