package budget

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/kailas-cloud/vecdex/internal/db"
)

// store is the consumer interface for budget operations (ISP).
type store interface {
	Get(ctx context.Context, key string) ([]byte, error)
	IncrBy(ctx context.Context, key string, val int64) error
	Expire(ctx context.Context, key string, ttl time.Duration, nx bool) error
}

// Store implements BudgetStore on top of DB (INCRBY + GET with TTL).
type Store struct {
	store    store
	dailyTTL time.Duration
	monthTTL time.Duration
}

// New creates a budget store.
// dailyTTL is the TTL for daily keys (recommended: 48h).
// monthTTL is the TTL for monthly keys (recommended: 62 days).
func New(s store, dailyTTL, monthTTL time.Duration) *Store {
	return &Store{
		store:    s,
		dailyTTL: dailyTTL,
		monthTTL: monthTTL,
	}
}

// IncrBy atomically increments the key value and sets TTL.
func (s *Store) IncrBy(ctx context.Context, key string, val int64) error {
	if err := s.store.IncrBy(ctx, key, val); err != nil {
		return fmt.Errorf("budget INCRBY %s: %w", key, err)
	}

	// Set TTL only if the key has no expiry yet (NX — not reset on repeat).
	ttl := s.ttlForKey(key)
	if err := s.store.Expire(ctx, key, ttl, true); err != nil {
		return fmt.Errorf("budget EXPIRE %s: %w", key, err)
	}

	return nil
}

// Get returns the current budget value. Returns 0 if the key does not exist.
func (s *Store) Get(ctx context.Context, key string) (int64, error) {
	data, err := s.store.Get(ctx, key)
	if err != nil {
		if errors.Is(err, db.ErrKeyNotFound) {
			return 0, nil
		}
		return 0, fmt.Errorf("budget GET %s: %w", key, err)
	}

	val, err := strconv.ParseInt(string(data), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("budget GET %s parse: %w", key, err)
	}
	return val, nil
}

// ttlForKey determines TTL based on the key format (daily vs monthly).
func (s *Store) ttlForKey(key string) time.Duration {
	// Keys follow the pattern vecdex:budget:{provider}:daily:... or :monthly:...
	if strings.Contains(key, ":daily:") {
		return s.dailyTTL
	}
	return s.monthTTL
}
