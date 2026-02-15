package embcache

import (
	"context"
	"testing"

	"github.com/kailas-cloud/vecdex/internal/db"
	"github.com/kailas-cloud/vecdex/internal/domain"
	"go.uber.org/zap"
)

type mockEmbedder struct {
	result domain.EmbeddingResult
	err    error
}

func (m *mockEmbedder) Embed(_ context.Context, _ string) (domain.EmbeddingResult, error) {
	return m.result, m.err
}

// mockKVStore implements the consumer interface for tests.
type mockKVStore struct {
	getFn func(ctx context.Context, key string) ([]byte, error)
	setFn func(ctx context.Context, key string, value []byte) error
}

func (m *mockKVStore) Get(ctx context.Context, key string) ([]byte, error) {
	if m.getFn != nil {
		return m.getFn(ctx, key)
	}
	return nil, db.ErrKeyNotFound
}

func (m *mockKVStore) Set(ctx context.Context, key string, value []byte) error {
	if m.setFn != nil {
		return m.setFn(ctx, key, value)
	}
	return nil
}

func newTestCachedEmbedder(t *testing.T, inner *mockEmbedder) (*CachedEmbedder, *mockKVStore) {
	t.Helper()
	ms := &mockKVStore{}
	ce := New(inner, ms, nil, zap.NewNop())
	return ce, ms
}
