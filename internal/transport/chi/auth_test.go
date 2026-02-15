package chi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	gen "github.com/kailas-cloud/vecdex/internal/transport/generated"
)

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
}

func TestAuthMiddleware_EmptyKeys_PassThrough(t *testing.T) {
	mw := BearerAuthMiddleware(nil)
	handler := mw(okHandler())

	req := httptest.NewRequest("GET", "/collections", http.NoBody)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("empty keys: got %d, want %d", rr.Code, http.StatusOK)
	}
}

func TestAuthMiddleware_EmptyStringKeys_PassThrough(t *testing.T) {
	mw := BearerAuthMiddleware([]string{"", ""})
	handler := mw(okHandler())

	req := httptest.NewRequest("GET", "/collections", http.NoBody)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("empty string keys: got %d, want %d", rr.Code, http.StatusOK)
	}
}

func TestAuthMiddleware_MissingHeader_401(t *testing.T) {
	mw := BearerAuthMiddleware([]string{"secret"})
	handler := mw(okHandler())

	req := httptest.NewRequest("GET", "/collections", http.NoBody)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("missing header: got %d, want %d", rr.Code, http.StatusUnauthorized)
	}

	var errResp gen.ErrorResponse
	if err := json.NewDecoder(rr.Body).Decode(&errResp); err != nil {
		t.Fatalf("decode error response: %v", err)
	}
	if errResp.Code != gen.ErrorResponseCodeBadRequest {
		t.Errorf("error code: got %s, want %s", errResp.Code, gen.ErrorResponseCodeBadRequest)
	}
}

func TestAuthMiddleware_BasicScheme_401(t *testing.T) {
	mw := BearerAuthMiddleware([]string{"secret"})
	handler := mw(okHandler())

	req := httptest.NewRequest("GET", "/collections", http.NoBody)
	req.Header.Set("Authorization", "Basic dXNlcjpwYXNz")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("basic scheme: got %d, want %d", rr.Code, http.StatusUnauthorized)
	}
}

func TestAuthMiddleware_InvalidToken_401(t *testing.T) {
	mw := BearerAuthMiddleware([]string{"secret"})
	handler := mw(okHandler())

	req := httptest.NewRequest("GET", "/collections", http.NoBody)
	req.Header.Set("Authorization", "Bearer wrong-key")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("invalid token: got %d, want %d", rr.Code, http.StatusUnauthorized)
	}
}

func TestAuthMiddleware_ValidToken_200(t *testing.T) {
	mw := BearerAuthMiddleware([]string{"secret"})
	handler := mw(okHandler())

	req := httptest.NewRequest("GET", "/collections", http.NoBody)
	req.Header.Set("Authorization", "Bearer secret")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("valid token: got %d, want %d", rr.Code, http.StatusOK)
	}
}

func TestAuthMiddleware_MultipleKeys(t *testing.T) {
	mw := BearerAuthMiddleware([]string{"key1", "key2"})
	handler := mw(okHandler())

	for _, key := range []string{"key1", "key2"} {
		req := httptest.NewRequest("GET", "/collections", http.NoBody)
		req.Header.Set("Authorization", "Bearer "+key)
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)

		if rr.Code != http.StatusOK {
			t.Errorf("key %s: got %d, want %d", key, rr.Code, http.StatusOK)
		}
	}
}

func TestAuthMiddleware_ExemptPaths(t *testing.T) {
	mw := BearerAuthMiddleware([]string{"secret"})
	handler := mw(okHandler())

	for _, path := range []string{"/health", "/metrics"} {
		req := httptest.NewRequest("GET", path, http.NoBody)
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)

		if rr.Code != http.StatusOK {
			t.Errorf("exempt path %s: got %d, want %d", path, rr.Code, http.StatusOK)
		}
	}
}
