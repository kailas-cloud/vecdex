package valkey

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
			key := "<unknown>"
			if i < len(keys) {
				key = keys[i]
			}
			return nil, fmt.Errorf("HGetAllMulti key %s: %w", key, err)
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

// HGetFieldMulti reads one small field per key in a pipeline, avoiding full document hydration.
func (s *Store) HGetFieldMulti(ctx context.Context, keys []string, field string) ([]string, error) {
	commands := make([]rueidis.Completed, len(keys))
	for i, key := range keys {
		commands[i] = s.b().Hget().Key(key).Field(field).Build()
	}
	out := make([]string, len(keys))
	for i, result := range s.client.DoMulti(ctx, commands...) {
		value, err := result.ToString()
		if rueidis.IsRedisNil(err) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("read hash field %s: %w", field, err)
		}
		out[i] = value
	}
	return out, nil
}

// HGetFieldsMulti reads each parent's projection and version atomically in one
// HMGET, while pipelining independent document partitions across the cluster.
func (s *Store) HGetFieldsMulti(ctx context.Context, keys, fields []string) ([][]string, error) {
	if len(fields) == 0 {
		return nil, fmt.Errorf("hash projection requires at least one field")
	}
	commands := make([]rueidis.Completed, len(keys))
	for i, key := range keys {
		commands[i] = s.b().Hmget().Key(key).Field(fields...).Build()
	}
	replies := s.client.DoMulti(ctx, commands...)
	if len(replies) != len(keys) {
		return nil, fmt.Errorf("incomplete hash projection response")
	}
	rows := make([][]string, len(keys))
	for i, reply := range replies {
		row, err := reply.ToArray()
		if err != nil {
			return nil, fmt.Errorf("read hash projection: %w", err)
		}
		if len(row) != len(fields) {
			return nil, fmt.Errorf("invalid hash projection width")
		}
		rows[i] = make([]string, len(fields))
		for j, message := range row {
			value, err := message.ToString()
			if err != nil && !rueidis.IsRedisNil(err) {
				return nil, fmt.Errorf("decode hash projection: %w", err)
			}
			rows[i][j] = value
		}
	}
	return rows, nil
}
