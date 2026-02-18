package redis

import (
	"context"
	"fmt"

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

// JSONSetMulti stores multiple JSON documents in a single DoMulti round-trip.
func (s *Store) JSONSetMulti(ctx context.Context, items []db.JSONSetItem) error {
	if len(items) == 0 {
		return nil
	}

	cmds := make([]rueidis.Completed, len(items))
	for i, item := range items {
		cmds[i] = s.b().Arbitrary("JSON.SET").Keys(item.Key).Args(item.Path, string(item.Data)).Build()
	}

	results := s.client.DoMulti(ctx, cmds...)
	for i, res := range results {
		if err := res.Error(); err != nil {
			return &db.Error{Op: db.OpJSONSet, Err: fmt.Errorf("key %s: %w", items[i].Key, err)}
		}
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
