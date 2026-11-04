package domain

import (
	"context"
	"sync"
)

type embeddingUsageKey struct{}

// EmbeddingUsage collects token usage for a single HTTP request.
// The handler puts a mutable pointer into the context before calling the service;
// the service writes after embedding; the handler reads it for response headers.
type EmbeddingUsage struct {
	mu          sync.Mutex
	Unknown     bool
	TotalTokens int
	Used        bool // true if embedding was called, even on a cache hit with 0 tokens
}

// NewContextWithUsage returns a context with an embedded usage collector.
func NewContextWithUsage(ctx context.Context) (context.Context, *EmbeddingUsage) {
	u := &EmbeddingUsage{}
	return context.WithValue(ctx, embeddingUsageKey{}, u), u
}

// UsageFromContext extracts the usage collector from context. Returns nil if not set.
func UsageFromContext(ctx context.Context) *EmbeddingUsage {
	u, _ := ctx.Value(embeddingUsageKey{}).(*EmbeddingUsage)
	return u
}

// AddTokens records consumed tokens.
func (u *EmbeddingUsage) AddTokens(n int) {
	if u != nil {
		u.mu.Lock()
		defer u.mu.Unlock()
		u.TotalTokens += n
		u.Used = true
	}
}

// AddEmbedding records known tokens and whether any upstream usage was unavailable.
func (u *EmbeddingUsage) AddEmbedding(tokens int, unknown bool) {
	if u == nil {
		return
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	u.TotalTokens += tokens
	u.Unknown = u.Unknown || unknown
	u.Used = true
}
