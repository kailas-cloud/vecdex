package config

import (
	"fmt"
	"strings"
	"unicode"
)

// Validate rejects ambiguous identities and refuses an accidentally open server.
func (a AuthConfig) Validate() error {
	if len(a.Tokens) == 0 {
		return fmt.Errorf("auth.tokens requires at least one principal credential")
	}
	keys := make(map[string]bool, len(a.Tokens))
	roles := make(map[string]string, len(a.Tokens))
	for _, token := range a.Tokens {
		if token.PrincipalID == "" || len(token.PrincipalID) > 128 ||
			strings.IndexFunc(token.PrincipalID, unicode.IsSpace) >= 0 {
			return fmt.Errorf("principal_id must contain 1–128 non-whitespace bytes")
		}
		if token.Role != "read" && token.Role != "write" && token.Role != "admin" {
			return fmt.Errorf("principal %q role must be read, write or admin", token.PrincipalID)
		}
		if token.Key == "" || strings.TrimSpace(token.Key) != token.Key || strings.ContainsAny(token.Key, "\r\n\t") {
			return fmt.Errorf("principal %q requires a non-empty Bearer key", token.PrincipalID)
		}
		if keys[token.Key] {
			return fmt.Errorf("auth.tokens contains a duplicate key")
		}
		if role, ok := roles[token.PrincipalID]; ok && role != token.Role {
			return fmt.Errorf("principal %q has inconsistent roles", token.PrincipalID)
		}
		keys[token.Key], roles[token.PrincipalID] = true, token.Role
	}
	return nil
}
