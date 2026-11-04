package config

import (
	"crypto/sha256"
	"fmt"
	"strconv"
	"strings"
)

// Fingerprint identifies compatible document/query vectors without including credentials.
// Quoted fields preserve boundaries even when instructions contain delimiters.
func (v VectorizerConfig) Fingerprint(provider *ProviderConfig) string {
	backend := provider.Backend
	if backend == "" {
		backend = "openai"
	}
	endpoint := strings.TrimRight(provider.BaseURL, "/")
	if backend == "openai" && endpoint == "" {
		endpoint = "https://api.openai.com/v1"
	}
	dimensions := v.Dimensions
	if dimensions == 0 {
		dimensions = 1024
	}
	identity := []string{"vecdex-space-v1", backend, endpoint, provider.Region, provider.ModelDir,
		strconv.Itoa(provider.MaxLength), v.Model, strconv.Itoa(dimensions),
		"search_document", v.DocumentInstruction, "search_query", v.QueryInstruction}
	return fmt.Sprintf("%x", sha256.Sum256([]byte(fmt.Sprintf("%q", identity))))
}
