package db

import (
	"context"
	"time"
)

// KVResult preserves per-key failures in a pipelined read.
type KVResult struct {
	Value []byte
	Err   error
}

// KVItem is one cache entry in a pipelined write.
type KVItem struct {
	Key   string
	Value []byte
}

// KVBatchStore groups independent cache commands without requiring a common hash slot.
type KVBatchStore interface {
	GetMulti(ctx context.Context, keys []string) []KVResult
	SetMultiWithTTL(ctx context.Context, items []KVItem, ttl time.Duration) error
}
