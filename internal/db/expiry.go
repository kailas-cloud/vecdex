package db

import "context"

// ExpiryStore reads the partition-local deadline catalogs. Persistent sources
// use +inf; only actual deadlines are returned as due work.
type ExpiryStore interface {
	ExpiryIDs(ctx context.Context, key string, before int64, limit int) ([]string, error)
	ExpiryIDPages(ctx context.Context, keys []string, before int64, limit int) ([][]string, error)
	LiveCounts(ctx context.Context, keys []string, now int64) ([]int, error)
}
