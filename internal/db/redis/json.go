package redis

import (
	"context"

	"github.com/redis/rueidis"

	"github.com/kailas-cloud/vecdex/internal/db"
)

// JSONSet stores a JSON document at the given key and path.
func (s *Store) JSONSet(ctx context.Context, key, path string, data []byte) error {
	cmd := s.b().Arbitrary("JSON.SET").Keys(key).Args(path, string(data)).Build()
	if err := s.do(ctx, cmd).Error(); err != nil {
		return &db.Error{Op: db.OpJSONSet, Err: err}
	}
	return nil
}

// JSONGet retrieves a JSON document by key and optional paths.
func (s *Store) JSONGet(ctx context.Context, key string, paths ...string) ([]byte, error) {
	args := make([]string, len(paths))
	copy(args, paths)

	cmd := s.b().Arbitrary("JSON.GET").Keys(key).Args(args...).Build()
	raw, err := s.do(ctx, cmd).ToString()
	if err != nil {
		if rueidis.IsRedisNil(err) {
			return nil, db.ErrKeyNotFound
		}
		return nil, &db.Error{Op: db.OpJSONGet, Err: err}
	}
	if raw == "" {
		return nil, db.ErrKeyNotFound
	}
	return []byte(raw), nil
}
