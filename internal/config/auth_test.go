package config

import "testing"

func TestAuthIdentityValidation(t *testing.T) {
	tests := []struct {
		name   string
		tokens []AuthToken
		valid  bool
	}{
		{"no credentials", nil, false},
		{"read", []AuthToken{{"reader", "read", "secret"}}, true},
		{"write", []AuthToken{{"writer", "write", "secret"}}, true},
		{"rotation", []AuthToken{{"team", "admin", "old"}, {"team", "admin", "new"}}, true},
		{"role escalation", []AuthToken{{"team", "read", "old"}, {"team", "admin", "new"}}, false},
		{"ambiguous key", []AuthToken{{"one", "read", "same"}, {"two", "write", "same"}}, false},
		{"missing owner", []AuthToken{{"", "read", "secret"}}, false},
		{"owner whitespace", []AuthToken{{"team\u00a0name", "read", "secret"}}, false},
		{"header injection", []AuthToken{{"team", "read", "secret\nheader"}}, false},
		{"unknown role", []AuthToken{{"team", "root", "secret"}}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := (AuthConfig{Tokens: tt.tokens}).Validate(); (err == nil) != tt.valid {
				t.Fatalf("valid=%v, error=%v", tt.valid, err)
			}
		})
	}
}
