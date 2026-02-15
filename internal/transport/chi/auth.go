package chi

import (
	"net/http"
	"strings"

	gen "github.com/kailas-cloud/vecdex/internal/transport/generated"
)

// exemptPaths are routes that bypass authentication (health, metrics).
var exemptPaths = map[string]struct{}{
	"/health":  {},
	"/metrics": {},
}

// BearerAuthMiddleware returns a middleware that validates Bearer tokens.
// If apiKeys is empty, authentication is disabled (pass-through).
func BearerAuthMiddleware(apiKeys []string) func(http.Handler) http.Handler {
	validKeys := make(map[string]struct{}, len(apiKeys))
	for _, k := range apiKeys {
		if k != "" {
			validKeys[k] = struct{}{}
		}
	}

	return func(next http.Handler) http.Handler {
		// Auth disabled — pass everything through
		if len(validKeys) == 0 {
			return next
		}

		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Exempt paths
			if _, ok := exemptPaths[r.URL.Path]; ok {
				next.ServeHTTP(w, r)
				return
			}

			auth := r.Header.Get("Authorization")
			if auth == "" {
				writeError(w, http.StatusUnauthorized, gen.ErrorResponseCodeBadRequest, "missing authorization header")
				return
			}

			const bearerPrefix = "Bearer "
			if !strings.HasPrefix(auth, bearerPrefix) {
				writeError(w, http.StatusUnauthorized,
					gen.ErrorResponseCodeBadRequest, "authorization header must use Bearer scheme")
				return
			}

			token := auth[len(bearerPrefix):]
			if _, ok := validKeys[token]; !ok {
				writeError(w, http.StatusUnauthorized, gen.ErrorResponseCodeBadRequest, "invalid api key")
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}
