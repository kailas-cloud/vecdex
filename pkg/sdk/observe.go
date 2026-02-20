package vecdex

import (
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// sdkMetrics holds prometheus metrics registered for the SDK.
type sdkMetrics struct {
	operations *prometheus.CounterVec
	duration   *prometheus.HistogramVec
}

func newSDKMetrics(reg prometheus.Registerer) (*sdkMetrics, error) {
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
	if err := registerOrReuse(reg, &m.operations); err != nil {
		return nil, err
	}
	if err := registerOrReuse(reg, &m.duration); err != nil {
		return nil, err
	}
	return m, nil
}

// registerOrReuse registers a collector or reuses an existing one.
func registerOrReuse[T prometheus.Collector](reg prometheus.Registerer, c *T) error {
	if err := reg.Register(*c); err != nil {
		var are prometheus.AlreadyRegisteredError
		if errors.As(err, &are) {
			existing, ok := are.ExistingCollector.(T)
			if !ok {
				return fmt.Errorf("vecdex: metric already registered with incompatible type: %T", are.ExistingCollector)
			}
			*c = existing
			return nil
		}
		return fmt.Errorf("vecdex: register metric: %w", err)
	}
	return nil
}

// observer provides logging and metrics for SDK operations.
type observer struct {
	logger  *slog.Logger
	metrics *sdkMetrics
}

func newObserver(logger *slog.Logger, reg prometheus.Registerer) (*observer, error) {
	var m *sdkMetrics
	if reg != nil {
		var err error
		m, err = newSDKMetrics(reg)
		if err != nil {
			return nil, err
		}
	}
	return &observer{logger: logger, metrics: m}, nil
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
