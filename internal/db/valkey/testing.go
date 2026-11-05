package valkey

import (
	"context"

	"github.com/redis/rueidis"

	"github.com/kailas-cloud/vecdex/internal/db"
)

// NewStoreForTest creates a Store with the provided rueidis client (test-only).
func NewStoreForTest(c rueidis.Client) *Store {
	return &Store{client: c}
}

// ScanForTest enumerates keys on an isolated single-node fixture. It is excluded
// from db.Store and must not be used by request or maintenance paths.
func (s *Store) ScanForTest(ctx context.Context, pattern string) ([]string, error) {
	var keys []string
	var cursor uint64

	for {
		cmd := s.b().Scan().Cursor(cursor).Match(pattern).Count(100).Build()
		res, err := s.do(ctx, cmd).AsScanEntry()
		if err != nil {
			return nil, &db.Error{Op: db.OpScan, Err: err}
		}
		keys = append(keys, res.Elements...)
		cursor = res.Cursor
		if cursor == 0 {
			break
		}
	}

	return keys, nil
}
