package config

import "testing"

func TestEmbeddingFingerprint(t *testing.T) {
	provider := ProviderConfig{Backend: "bedrock", Region: "eu-west-1", APIKey: "secret"}
	vectorizer := VectorizerConfig{Model: "cohere.embed-v4:0", Dimensions: 1024}
	original := vectorizer.Fingerprint(&provider)
	for _, tc := range []struct {
		name   string
		change func(*ProviderConfig, *VectorizerConfig)
	}{
		{"region", func(p *ProviderConfig, _ *VectorizerConfig) { p.Region = "us-east-1" }},
		{"endpoint", func(p *ProviderConfig, _ *VectorizerConfig) { p.BaseURL = "https://other" }},
		{"backend", func(p *ProviderConfig, _ *VectorizerConfig) { p.Backend = "openai" }},
		{"model", func(_ *ProviderConfig, v *VectorizerConfig) { v.Model = "other" }},
		{"dimensions", func(_ *ProviderConfig, v *VectorizerConfig) { v.Dimensions = 256 }},
		{"document instructions", func(_ *ProviderConfig, v *VectorizerConfig) { v.DocumentInstruction = "document:" }},
		{"query instructions", func(_ *ProviderConfig, v *VectorizerConfig) { v.QueryInstruction = "query:" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, v := provider, vectorizer
			tc.change(&p, &v)
			if v.Fingerprint(&p) == original {
				t.Fatal("incompatible configuration shares a fingerprint")
			}
		})
	}
	provider.APIKey = "rotated"
	provider.CacheTTLSec = 1
	provider.TimeoutSec = 90
	provider.MaxConcurrency = 8
	if vectorizer.Fingerprint(&provider) != original {
		t.Fatal("credential rotation or transport settings changed vector identity")
	}
	defaultProvider, explicitProvider := ProviderConfig{}, ProviderConfig{
		Backend: "openai",
		BaseURL: "https://api.openai.com/v1/",
	}
	if vectorizer.Fingerprint(&defaultProvider) != vectorizer.Fingerprint(&explicitProvider) {
		t.Fatal("equivalent endpoint defaults differ")
	}
}
