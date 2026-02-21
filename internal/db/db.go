package db

import (
	"context"
	"time"
)

// Store is the main database facade combining all sub-interfaces.
//
//nolint:interfacebloat // facade by design -- consumers use narrow sub-interfaces (ISP)
type Store interface {
	Pinger
	HashStore
	JSONStore
	KVStore
	IndexManager
	Searcher
	Close()
	WaitForReady(ctx context.Context, timeout time.Duration) error
}

// Pinger checks database connectivity.
type Pinger interface {
	Ping(ctx context.Context) error
}

// HashSetItem holds a single key+fields pair for pipelined HSET.
type HashSetItem struct {
	Key    string
	Fields map[string]string
}

// HashStore provides hash-based key-value operations.
type HashStore interface {
	HSet(ctx context.Context, key string, fields map[string]string) error
	HSetMulti(ctx context.Context, items []HashSetItem) error
	HGetAll(ctx context.Context, key string) (map[string]string, error)
	HGetAllMulti(ctx context.Context, keys []string) ([]map[string]string, error)
	Del(ctx context.Context, key string) error
	Exists(ctx context.Context, key string) (bool, error)
	Scan(ctx context.Context, pattern string) ([]string, error)
}

// JSONSetItem holds a single key+path+data triple for pipelined JSON.SET.
type JSONSetItem struct {
	Key  string
	Path string
	Data []byte
}

// JSONStore provides JSON document operations.
type JSONStore interface {
	JSONSet(ctx context.Context, key, path string, data []byte) error
	JSONSetMulti(ctx context.Context, items []JSONSetItem) error
	JSONGet(ctx context.Context, key string, paths ...string) ([]byte, error)
	Del(ctx context.Context, key string) error
	Exists(ctx context.Context, key string) (bool, error)
}

// KVStore provides simple key-value operations.
type KVStore interface {
	Get(ctx context.Context, key string) ([]byte, error)
	Set(ctx context.Context, key string, value []byte) error
	SetWithTTL(ctx context.Context, key string, value []byte, ttl time.Duration) error
	IncrBy(ctx context.Context, key string, val int64) error
	Expire(ctx context.Context, key string, ttl time.Duration, nx bool) error
}

// IndexManager provides FT index lifecycle operations.
type IndexManager interface {
	CreateIndex(ctx context.Context, def *IndexDefinition) error
	DropIndex(ctx context.Context, name string) error
	IndexExists(ctx context.Context, name string) (bool, error)
	SupportsTextSearch(ctx context.Context) bool
}

// Searcher provides search operations over FT indexes.
type Searcher interface {
	SearchKNN(ctx context.Context, q *KNNQuery) (*SearchResult, error)
	SearchBM25(ctx context.Context, q *TextQuery) (*SearchResult, error)
	SearchList(ctx context.Context, index, query string, offset, limit int, fields []string) (*SearchResult, error)
	SearchCount(ctx context.Context, index, query string) (int, error)
}
