package metrics

import (
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/prometheus/client_golang/prometheus"
)

var (
	httpRequestDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: "vecdex",
			Name:      "http_request_duration_seconds",
			Help:      "HTTP request duration in seconds",
			Buckets:   []float64{0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10},
		},
		[]string{"method", "path", "status"},
	)

	httpRequestsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "vecdex",
			Name:      "http_requests_total",
			Help:      "Total number of HTTP requests",
		},
		[]string{"method", "path", "status"},
	)
)

func init() {
	prometheus.MustRegister(httpRequestDuration)
	prometheus.MustRegister(httpRequestsTotal)
}

// Middleware records HTTP request duration and count.
func Middleware() func(next http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()

			// Preserve Flusher and ResponseController support for MCP SSE streams.
			ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
			next.ServeHTTP(ww, r)

			duration := time.Since(start).Seconds()
			status := strconv.Itoa(ww.Status())

			// Use chi route pattern for path normalization
			routePattern := chi.RouteContext(r.Context()).RoutePattern()
			path := normalizePath(routePattern)
			method := r.Method

			httpRequestDuration.WithLabelValues(method, path, status).Observe(duration)
			httpRequestsTotal.WithLabelValues(method, path, status).Inc()
		})
	}
}

// normalizePath normalizes paths to prevent high cardinality in metrics labels.
func normalizePath(path string) string {
	if path == "" {
		return "unknown"
	}
	return path
}
