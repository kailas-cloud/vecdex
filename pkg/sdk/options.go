package vecdex

import (
	"log/slog"

	"github.com/prometheus/client_golang/prometheus"
)

// Option configures the Client.
type Option interface {
	apply(*clientConfig)
}

// optionFunc adapts a function to the Option interface.
type optionFunc func(*clientConfig)

func (f optionFunc) apply(c *clientConfig) { f(c) }

type clientConfig struct {
	driver     string // "valkey" or "redis"
	addrs      []string
	password   string
	standalone bool

	embedder Embedder

	vectorDimensions int
	hnswM            int
	hnswEFConstruct  int
	maxBatchSize     int

	logger     *slog.Logger
	metricsReg prometheus.Registerer
}

// WithValkey configures the client to connect to a Valkey instance.
func WithValkey(addr, password string) Option {
	return optionFunc(func(c *clientConfig) {
		c.driver = "valkey"
		c.addrs = []string{addr}
		c.password = password
	})
}

// WithRedis configures the client to connect to a Redis instance.
func WithRedis(addr, password string) Option {
	return optionFunc(func(c *clientConfig) {
		c.driver = "redis"
		c.addrs = []string{addr}
		c.password = password
	})
}

// WithStandalone disables cluster topology discovery.
// Use for standalone Valkey/Redis instances (not managed by cluster operator).
func WithStandalone() Option {
	return func(c *clientConfig) {
		c.standalone = true
	}
}

// WithEmbedder sets the text embedding provider.
// Required for text collections; geo collections work without it.
func WithEmbedder(e Embedder) Option {
	return optionFunc(func(c *clientConfig) {
		c.embedder = e
	})
}

// WithVectorDimensions sets the default vector dimension for text collections.
// Defaults to 1024 (Qwen3-Embedding-8B).
func WithVectorDimensions(dim int) Option {
	return optionFunc(func(c *clientConfig) {
		c.vectorDimensions = dim
	})
}

// WithHNSW configures HNSW index parameters (M and EF construction).
// Defaults: M=32, EFConstruct=400.
func WithHNSW(m, efConstruct int) Option {
	return optionFunc(func(c *clientConfig) {
		c.hnswM = m
		c.hnswEFConstruct = efConstruct
	})
}

// WithMaxBatchSize sets the maximum number of items per batch operation.
// Default: 100.
func WithMaxBatchSize(size int) Option {
	return optionFunc(func(c *clientConfig) {
		c.maxBatchSize = size
	})
}

// WithLogger enables structured logging for SDK operations.
// Pass nil to disable (default). Uses standard library slog.
func WithLogger(l *slog.Logger) Option {
	return optionFunc(func(c *clientConfig) {
		c.logger = l
	})
}

// WithPrometheus registers SDK metrics (operation counts and durations)
// on the given registerer. Pass nil to disable (default).
func WithPrometheus(reg prometheus.Registerer) Option {
	return optionFunc(func(c *clientConfig) {
		c.metricsReg = reg
	})
}
