package valkey

import (
	"context"
	"fmt"
	"time"

	"github.com/kailas-cloud/vecdex/internal/db"
	"github.com/redis/rueidis"
)

// GetMulti pipelines GETs, including keys in different cluster hash slots.
func (s *Store) GetMulti(ctx context.Context, keys []string) []db.KVResult {
	if len(keys) == 0 {
		return nil
	}
	commands := make([]rueidis.Completed, len(keys))
	for i, key := range keys {
		commands[i] = s.b().Get().Key(key).Build()
	}
	replies := s.client.DoMulti(ctx, commands...)
	results := make([]db.KVResult, len(replies))
	for i, reply := range replies {
		value, err := reply.AsBytes()
		if rueidis.IsRedisNil(err) {
			err = db.ErrKeyNotFound
		}
		results[i] = db.KVResult{Value: value, Err: err}
	}
	return results
}

// SetMultiWithTTL pipelines writes and expiration in one SET per unique entry.
func (s *Store) SetMultiWithTTL(ctx context.Context, items []db.KVItem, ttl time.Duration) error {
	if len(items) == 0 {
		return nil
	}
	commands := make([]rueidis.Completed, len(items))
	for i, item := range items {
		commands[i] = s.b().Set().Key(item.Key).Value(string(item.Value)).Px(ttl).Build()
	}
	replies := s.client.DoMulti(ctx, commands...)
	for i, reply := range replies {
		if err := reply.Error(); err != nil {
			return &db.Error{Op: db.OpSet, Err: fmt.Errorf("pipeline key %s: %w", items[i].Key, err)}
		}
	}
	return nil
}
