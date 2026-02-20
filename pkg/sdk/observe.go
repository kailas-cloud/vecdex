package vecdex

import (
	"log/slog"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// sdkMetrics holds prometheus metrics registered for the SDK.
type sdkMetrics struct {
	operations *prometheus.CounterVec
	duration   *prometheus.HistogramVec
}

func newSDKMetrics(reg prometheus.Registerer) *sdkMetrics {
	m := &sdkMetrics{
		operations: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "vecdex",
			Subsystem: "sdk",
			Name:      "operations_total",
			Help:      "Total SDK operations by type and status.",
		}, []string{"operation", "status"}),
		duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "vecdex",
			Subsystem: "sdk",
			Name:      "operation_duration_seconds",
			Help:      "SDK operation duration in seconds.",
			Buckets:   prometheus.DefBuckets,
		}, []string{"operation"}),
	}
	reg.MustRegister(m.operations, m.duration)
	return m
}

// observer provides logging and metrics for SDK operations.
type observer struct {
	logger  *slog.Logger
	metrics *sdkMetrics
}

func newObserver(logger *slog.Logger, reg prometheus.Registerer) *observer {
	var m *sdkMetrics
	if reg != nil {
		m = newSDKMetrics(reg)
	}
	return &observer{logger: logger, metrics: m}
}

func (o *observer) observe(
	op string, start time.Time, err error,
) {
	if o == nil {
		return
	}
	dur := time.Since(start)

	if o.metrics != nil {
		status := "ok"
		if err != nil {
			status = "error"
		}
		o.metrics.operations.WithLabelValues(op, status).Inc()
		o.metrics.duration.WithLabelValues(op).Observe(
			dur.Seconds(),
		)
	}

	if o.logger != nil {
		if err != nil {
			o.logger.Warn("operation failed",
				"op", op,
				"duration", dur,
				"error", err,
			)
		} else {
			o.logger.Debug("operation completed",
				"op", op,
				"duration", dur,
			)
		}
	}
}
