package valkey

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/redis/rueidis"

	"github.com/kailas-cloud/vecdex/internal/db"
)

// Compile-time check: Store implements db.Store.
var _ db.Store = (*Store)(nil)

// Config holds connection parameters for a Valkey store.
type Config struct {
	Addrs      []string
	Username   string
	Password   string
	DB         int
	Standalone bool // true для standalone Valkey (без CLUSTER topology discovery)
}

// Store implements db.Store via rueidis for Valkey (valkey-search).
type Store struct {
	client     rueidis.Client
	queryRoute atomic.Uint64
}

// NewStore creates a Valkey store via rueidis.
func NewStore(cfg Config) (*Store, error) {
	if len(cfg.Addrs) == 0 {
		return nil, fmt.Errorf("addrs is required")
	}

	client, err := rueidis.NewClient(rueidis.ClientOption{
		InitAddress:       cfg.Addrs,
		Username:          cfg.Username,
		Password:          cfg.Password,
		SelectDB:          cfg.DB,
		DisableCache:      true,
		AlwaysRESP2:       true,           // FT.SEARCH result parsing expects RESP2 array format
		ForceSingleClient: cfg.Standalone, // standalone Valkey — не пробуем CLUSTER topology
		// A former primary can return as a replica without causing another MOVED.
		// Refresh membership in the background so readiness can rediscover it.
		ClusterOption: rueidis.ClusterOption{ShardsRefreshInterval: time.Second},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create client: %w", err)
	}

	store := &Store{client: client}
	var seed [8]byte
	if _, err := rand.Read(seed[:]); err != nil {
		client.Close()
		return nil, fmt.Errorf("initialize query routing: %w", err)
	}
	store.queryRoute.Store(binary.LittleEndian.Uint64(seed[:]))
	return store, nil
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

// Client returns the shared connection for colocated native context transactions.
// The Store owns its lifetime; callers must not close it.
func (s *Store) Client() rueidis.Client { return s.client }

// WaitForReady polls complete primary and Search readiness until the deadline.
func (s *Store) WaitForReady(ctx context.Context, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	var last error

	for {
		select {
		case <-ctx.Done():
			if last != nil {
				return fmt.Errorf("timeout waiting for valkey: %w: %w", last, ctx.Err())
			}
			return fmt.Errorf("timeout waiting for valkey: %w", ctx.Err())
		case <-ticker.C:
			if last = s.Ready(ctx); last == nil {
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

// isRedisErr checks if err is a Redis/Valkey server error containing substr (case-insensitive).
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
