package redis

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/rueidis"

	"github.com/kailas-cloud/vecdex/internal/db"
)

// Compile-time check: Store implements db.Store.
var _ db.Store = (*Store)(nil)

// Config holds connection parameters for a Redis store.
type Config struct {
	Addrs    []string
	Username string
	Password string
	DB       int
}

// Store implements db.Store via rueidis for Redis 8+.
type Store struct {
	client rueidis.Client
}

// NewStore creates a Redis store via rueidis.
func NewStore(cfg Config) (*Store, error) {
	if len(cfg.Addrs) == 0 {
		return nil, fmt.Errorf("addrs is required")
	}

	client, err := rueidis.NewClient(rueidis.ClientOption{
		InitAddress:  cfg.Addrs,
		Username:     cfg.Username,
		Password:     cfg.Password,
		SelectDB:     cfg.DB,
		DisableCache: true,
		AlwaysRESP2:  true, // FT.SEARCH result parsing expects RESP2 array format
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create client: %w", err)
	}

	return &Store{client: client}, nil
}

// Ping checks connectivity.
func (s *Store) Ping(ctx context.Context) error {
	cmd := s.client.B().Ping().Build()
	if err := s.client.Do(ctx, cmd).Error(); err != nil {
		return fmt.Errorf("ping: %w", err)
	}
	return nil
}

// Close shuts down the client.
func (s *Store) Close() {
	s.client.Close()
}

// WaitForReady polls Ping until the store responds or timeout expires.
func (s *Store) WaitForReady(ctx context.Context, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("timeout waiting for database: %w", ctx.Err())
		case <-ticker.C:
			if err := s.Ping(ctx); err == nil {
				return nil
			}
		}
	}
}

func (s *Store) do(ctx context.Context, cmd rueidis.Completed) rueidis.RedisResult {
	return s.client.Do(ctx, cmd)
}

func (s *Store) b() rueidis.Builder {
	return s.client.B()
}

// isRedisErr checks if err is a Redis server error containing substr (case-insensitive).
func isRedisErr(err error, substr string) bool {
	re, ok := rueidis.IsRedisErr(err)
	if !ok {
		return false
	}
	return containsIgnoreCase(re.Error(), substr)
}

func containsIgnoreCase(s, substr string) bool {
	ls := len(s)
	lsub := len(substr)
	if lsub > ls {
		return false
	}
	for i := 0; i <= ls-lsub; i++ {
		match := true
		for j := 0; j < lsub; j++ {
			sc := s[i+j]
			tc := substr[j]
			if sc >= 'A' && sc <= 'Z' {
				sc += 'a' - 'A'
			}
			if tc >= 'A' && tc <= 'Z' {
				tc += 'a' - 'A'
			}
			if sc != tc {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}
