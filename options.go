package vecdex

// Option configures the Client.
type Option func(*clientConfig)

type clientConfig struct {
	driver   string // "valkey" or "redis"
	addrs    []string
	password string

	embedder Embedder

	vectorDimensions int
	hnswM            int
	hnswEFConstruct  int
	maxBatchSize     int
}

// WithValkey configures the client to connect to a Valkey instance.
func WithValkey(addr, password string) Option {
	return func(c *clientConfig) {
		c.driver = "valkey"
		c.addrs = []string{addr}
		c.password = password
	}
}

// WithRedis configures the client to connect to a Redis instance.
func WithRedis(addr, password string) Option {
	return func(c *clientConfig) {
		c.driver = "redis"
		c.addrs = []string{addr}
		c.password = password
	}
}

// WithEmbedder sets the text embedding provider.
// Required for text collections; geo collections work without it.
func WithEmbedder(e Embedder) Option {
	return func(c *clientConfig) {
		c.embedder = e
	}
}

// WithVectorDimensions sets the default vector dimension for text collections.
// Defaults to 1024 (Qwen3-Embedding-8B).
func WithVectorDimensions(dim int) Option {
	return func(c *clientConfig) {
		c.vectorDimensions = dim
	}
}

// WithHNSW configures HNSW index parameters (M and EF construction).
// Defaults: M=32, EFConstruct=400.
func WithHNSW(m, efConstruct int) Option {
	return func(c *clientConfig) {
		c.hnswM = m
		c.hnswEFConstruct = efConstruct
	}
}

// WithMaxBatchSize sets the maximum number of items per batch operation.
// Default: 100.
func WithMaxBatchSize(size int) Option {
	return func(c *clientConfig) {
		c.maxBatchSize = size
	}
}
