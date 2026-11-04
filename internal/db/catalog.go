package db

import "context"

// CatalogStore reads parent IDs without scanning the database or search index.
type CatalogStore interface {
	CatalogIDs(ctx context.Context, key, after string, limit int) ([]string, error)
	CatalogCount(ctx context.Context, key string) (int, error)
}
