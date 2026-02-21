package redis

import (
	"context"
	"fmt"

	"github.com/redis/rueidis"

	"github.com/kailas-cloud/vecdex/internal/db"
)

// HSet sets hash fields.
func (s *Store) HSet(ctx context.Context, key string, fields map[string]string) error {
	cmd := s.b().Hset().Key(key).FieldValue()
	for k, v := range fields {
		cmd = cmd.FieldValue(k, v)
	}
	if err := s.do(ctx, cmd.Build()).Error(); err != nil {
		return &db.Error{Op: db.OpHSet, Err: err}
	}
	return nil
}

// HSetMulti stores multiple hashes in a single DoMulti round-trip.
func (s *Store) HSetMulti(ctx context.Context, items []db.HashSetItem) error {
	if len(items) == 0 {
		return nil
	}

	cmds := make([]rueidis.Completed, len(items))
	for i, item := range items {
		cmd := s.b().Hset().Key(item.Key).FieldValue()
		for k, v := range item.Fields {
			cmd = cmd.FieldValue(k, v)
		}
		cmds[i] = cmd.Build()
	}

	results := s.client.DoMulti(ctx, cmds...)
	for i, res := range results {
		if err := res.Error(); err != nil {
			return &db.Error{Op: db.OpHSet, Err: fmt.Errorf("key %s: %w", items[i].Key, err)}
		}
	}
	return nil
}

// HGetAll returns all fields of a hash.
func (s *Store) HGetAll(ctx context.Context, key string) (map[string]string, error) {
	cmd := s.b().Hgetall().Key(key).Build()
	m, err := s.do(ctx, cmd).AsStrMap()
	if err != nil {
		return nil, &db.Error{Op: db.OpHGetAll, Err: err}
	}
	return m, nil
}

// HGetAllMulti fetches all fields for multiple hashes in a single DoMulti round-trip.
func (s *Store) HGetAllMulti(ctx context.Context, keys []string) ([]map[string]string, error) {
	if len(keys) == 0 {
		return nil, nil
	}

	cmds := make([]rueidis.Completed, len(keys))
	for i, key := range keys {
		cmds[i] = s.b().Hgetall().Key(key).Build()
	}

	results := s.client.DoMulti(ctx, cmds...)
	out := make([]map[string]string, len(results))

	for i, res := range results {
		m, err := res.AsStrMap()
		if err != nil {
			return nil, fmt.Errorf("HGetAllMulti key %s: %w", keys[i], err)
		}
		out[i] = m
	}

	return out, nil
}

// HDel removes specific fields from a hash.
func (s *Store) HDel(ctx context.Context, key string, fields ...string) error {
	if len(fields) == 0 {
		return nil
	}
	cmd := s.b().Hdel().Key(key).Field(fields...).Build()
	if err := s.do(ctx, cmd).Error(); err != nil {
		return &db.Error{Op: db.OpHDel, Err: err}
	}
	return nil
}

// Del deletes a key.
func (s *Store) Del(ctx context.Context, key string) error {
	cmd := s.b().Del().Key(key).Build()
	if err := s.do(ctx, cmd).Error(); err != nil {
		return &db.Error{Op: db.OpDel, Err: err}
	}
	return nil
}

// Exists checks if a key exists.
func (s *Store) Exists(ctx context.Context, key string) (bool, error) {
	cmd := s.b().Exists().Key(key).Build()
	count, err := s.do(ctx, cmd).AsInt64()
	if err != nil {
		return false, &db.Error{Op: db.OpExists, Err: err}
	}
	return count > 0, nil
}

// Scan iterates keys matching a pattern.
func (s *Store) Scan(ctx context.Context, pattern string) ([]string, error) {
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
