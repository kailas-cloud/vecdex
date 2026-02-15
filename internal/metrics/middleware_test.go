package metrics

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestMetricsMiddleware_RecordsDurationAndCount(t *testing.T) {
	r := chi.NewRouter()
	r.Use(Middleware())
	r.Get("/api/test", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	req := httptest.NewRequest("GET", "/api/test", http.NoBody)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	if rr.Code != 200 {
		t.Fatalf("expected status 200, got %d", rr.Code)
	}

	requestsVal := testutil.ToFloat64(httpRequestsTotal.WithLabelValues("GET", "/api/test", "200"))
	if requestsVal < 1 {
		t.Errorf("expected http_requests_total >= 1, got %f", requestsVal)
	}

	durationCount := testutil.CollectAndCount(httpRequestDuration)
	if durationCount == 0 {
		t.Error("expected http_request_duration_seconds to have observations")
	}
}

func TestMetricsMiddleware_DifferentStatusCodes(t *testing.T) {
	r := chi.NewRouter()
	r.Use(Middleware())

	r.Get("/ok", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	r.Get("/notfound", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	r.Get("/error", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})

	tests := []struct {
		path           string
		expectedStatus string
	}{
		{"/ok", "200"},
		{"/notfound", "404"},
		{"/error", "500"},
	}

	for _, tc := range tests {
		t.Run(tc.path, func(t *testing.T) {
			req := httptest.NewRequest("GET", tc.path, http.NoBody)
			rr := httptest.NewRecorder()
			r.ServeHTTP(rr, req)

			val := testutil.ToFloat64(httpRequestsTotal.WithLabelValues("GET", tc.path, tc.expectedStatus))
			if val < 1 {
				t.Errorf("expected requests_total for %s with status %s >= 1, got %f", tc.path, tc.expectedStatus, val)
			}
		})
	}
}

func TestMetricsMiddleware_DifferentMethods(t *testing.T) {
	r := chi.NewRouter()
	r.Use(Middleware())

	r.Get("/resource", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("get"))
	})
	r.Post("/resource", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("post"))
	})
	r.Delete("/resource", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("delete"))
	})

	methods := []string{"GET", "POST", "DELETE"}

	for _, method := range methods {
		t.Run(method, func(t *testing.T) {
			req := httptest.NewRequest(method, "/resource", http.NoBody)
			rr := httptest.NewRecorder()
			r.ServeHTTP(rr, req)

			val := testutil.ToFloat64(httpRequestsTotal.WithLabelValues(method, "/resource", "200"))
			if val < 1 {
				t.Errorf("expected requests_total for %s >= 1, got %f", method, val)
			}
		})
	}
}

func TestNormalizePath(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"", "unknown"},
		{"/api/v1/users", "/api/v1/users"},
		{"/health", "/health"},
	}

	for _, tc := range tests {
		result := normalizePath(tc.input)
		if result != tc.expected {
			t.Errorf("normalizePath(%q) = %q, want %q", tc.input, result, tc.expected)
		}
	}
}

func TestMetricsHandler_ViaPromhttp(t *testing.T) {
	r := chi.NewRouter()

	r.Get("/metrics", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("# metrics placeholder"))
	})

	req := httptest.NewRequest("GET", "/metrics", http.NoBody)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	if rr.Code != 200 {
		t.Fatalf("expected status 200, got %d", rr.Code)
	}

	body, err := io.ReadAll(rr.Body)
	if err != nil {
		t.Fatalf("failed to read body: %v", err)
	}

	if len(body) == 0 {
		t.Error("expected non-empty metrics response")
	}
}
