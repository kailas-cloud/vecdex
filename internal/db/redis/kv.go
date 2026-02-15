package redis

import (
	"context"
	"time"

	"github.com/redis/rueidis"

	"github.com/kailas-cloud/vecdex/internal/db"
)

// Get retrieves a value by key.
func (s *Store) Get(ctx context.Context, key string) ([]byte, error) {
	cmd := s.b().Get().Key(key).Build()
	data, err := s.do(ctx, cmd).AsBytes()
	if err != nil {
		if rueidis.IsRedisNil(err) {
			return nil, db.ErrKeyNotFound
		}
		return nil, &db.Error{Op: db.OpGet, Err: err}
	}
	return data, nil
}

// Set stores a value at the given key.
func (s *Store) Set(ctx context.Context, key string, value []byte) error {
	cmd := s.b().Set().Key(key).Value(string(value)).Build()
	if err := s.do(ctx, cmd).Error(); err != nil {
		return &db.Error{Op: db.OpSet, Err: err}
	}
	return nil
}

// SetWithTTL stores a value with an expiration.
func (s *Store) SetWithTTL(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	cmd := s.b().Set().Key(key).Value(string(value)).Ex(ttl).Build()
	if err := s.do(ctx, cmd).Error(); err != nil {
		return &db.Error{Op: db.OpSet, Err: err}
	}
	return nil
}

// IncrBy atomically increments a key by the given amount.
func (s *Store) IncrBy(ctx context.Context, key string, val int64) error {
	cmd := s.b().Incrby().Key(key).Increment(val).Build()
	if err := s.do(ctx, cmd).Error(); err != nil {
		return &db.Error{Op: db.OpIncrBy, Err: err}
	}
	return nil
}

// Expire sets TTL on a key. When nx=true, sets TTL only if the key has no expiry yet (EXPIRE NX).
func (s *Store) Expire(ctx context.Context, key string, ttl time.Duration, nx bool) error {
	var cmd rueidis.Completed
	if nx {
		cmd = s.b().Expire().Key(key).Seconds(int64(ttl.Seconds())).Nx().Build()
	} else {
		cmd = s.b().Expire().Key(key).Seconds(int64(ttl.Seconds())).Build()
	}
	if err := s.do(ctx, cmd).Error(); err != nil {
		return &db.Error{Op: db.OpExpire, Err: err}
	}
	return nil
}
