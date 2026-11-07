// Package server exposes the MCP endpoint and operational HTTP routes.
package server

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.uber.org/zap"

	"github.com/kailas-cloud/vecdex/internal/app"
	"github.com/kailas-cloud/vecdex/internal/config"
	"github.com/kailas-cloud/vecdex/internal/metrics"
	"github.com/kailas-cloud/vecdex/internal/skills"
	transport "github.com/kailas-cloud/vecdex/internal/transport/mcp"
	healthuc "github.com/kailas-cloud/vecdex/internal/usecase/health"
	"github.com/kailas-cloud/vecdex/internal/version"
)

// Handler owns the MCP subscription lifecycle and router.
type Handler struct {
	http.Handler
	mcp *transport.Handler
}

// New binds the production services to authenticated MCP and public health routes.
func New(rt *app.Runtime, auth config.AuthConfig, logger *zap.Logger) (*Handler, error) {
	if err := auth.Validate(); err != nil {
		return nil, fmt.Errorf("MCP auth: %w", err)
	}
	bundle := skills.All()
	assets := make([]transport.Skill, len(bundle))
	for i, skill := range bundle {
		assets[i] = transport.Skill{Name: skill.Name, Description: skill.Description,
			Content: skill.Content, Files: skill.Files}
	}
	mcp, err := transport.New(transport.Dependencies{
		Collections: rt.Collections, Documents: rt.Documents, Search: rt.Search,
		Usage: rt.Usage, Health: rt.Health, Contexts: rt.Contexts,
	}, transport.Options{Version: version.Version, Skills: assets, Observer: observeMCP(logger)})
	if err != nil {
		return nil, fmt.Errorf("MCP transport: %w", err)
	}
	r := chi.NewRouter()
	r.Use(middleware.RequestID, middleware.Recoverer, metrics.Middleware(), accessLog(logger))
	r.Handle("/mcp", authenticate(auth, mcp))
	r.Get("/health", func(w http.ResponseWriter, request *http.Request) {
		report := rt.Health.Check(request.Context())
		w.Header().Set("Content-Type", "application/json")
		if report.Status != healthuc.Healthy {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
		_ = json.NewEncoder(w).Encode(struct {
			Status  healthuc.Status                 `json:"status"`
			Checks  map[string]healthuc.CheckResult `json:"checks"`
			Version string                          `json:"version"`
		}{report.Status, report.Checks, version.Version})
	})
	r.Handle("/metrics", promhttp.Handler())
	return &Handler{Handler: r, mcp: mcp}, nil
}

// Close closes active subscriptions before the HTTP server is drained.
func (h *Handler) Close() error {
	if err := h.mcp.Close(); err != nil {
		return fmt.Errorf("close MCP handler: %w", err)
	}
	return nil
}

type credential struct {
	digest    [32]byte
	principal transport.Principal
}

func authenticate(auth config.AuthConfig, next http.Handler) http.Handler {
	credentials := make([]credential, len(auth.Tokens))
	for i, token := range auth.Tokens {
		credentials[i] = credential{sha256.Sum256([]byte("Bearer " + token.Key)),
			transport.Principal{KeyID: token.PrincipalID, Role: token.Role}}
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		digest := sha256.Sum256([]byte(r.Header.Get("Authorization")))
		for _, candidate := range credentials {
			if subtle.ConstantTimeCompare(digest[:], candidate.digest[:]) == 1 {
				next.ServeHTTP(w, r.WithContext(transport.WithPrincipal(r.Context(), candidate.principal)))
				return
			}
		}
		w.Header().Set("WWW-Authenticate", `Bearer realm="vecdex"`)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"code": "unauthorized", "message": "valid Bearer credential required",
		})
	})
}

func accessLog(logger *zap.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			requestID := middleware.GetReqID(r.Context())
			w.Header().Set("X-Request-ID", requestID)
			wrapped := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
			next.ServeHTTP(wrapped, r)
			logger.Info("http_request", zap.String("request_id", requestID),
				zap.String("path", r.URL.Path), zap.String("method", r.Method),
				zap.Int("status", wrapped.Status()), zap.Int("response_bytes", wrapped.BytesWritten()),
				zap.Duration("latency", time.Since(start)))
		})
	}
}
