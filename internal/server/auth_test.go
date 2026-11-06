package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kailas-cloud/vecdex/internal/config"
	transport "github.com/kailas-cloud/vecdex/internal/transport/mcp"
)

func TestAuthenticationKeepsOwnerAcrossRotation(t *testing.T) {
	auth := config.AuthConfig{Tokens: []config.AuthToken{
		{PrincipalID: "same-owner", Role: "write", Key: "old-secret"},
		{PrincipalID: "same-owner", Role: "write", Key: "new-secret"},
	}}
	handler := authenticate(auth, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok := transport.PrincipalFromContext(r.Context())
		if !ok || p.KeyID != "same-owner" || p.Role != "write" {
			t.Errorf("principal: %+v, %v", p, ok)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	for _, key := range []string{"old-secret", "new-secret", "wrong", ""} {
		r := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/mcp", http.NoBody)
		r.Header.Set("Authorization", "Bearer "+key)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		want := http.StatusNoContent
		if key == "wrong" || key == "" {
			want = http.StatusUnauthorized
		}
		if w.Code != want {
			t.Fatalf("status=%d, want %d", w.Code, want)
		}
	}
}
