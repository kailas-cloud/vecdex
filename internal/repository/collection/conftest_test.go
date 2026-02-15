package collection

import (
	"context"
	"testing"

	"github.com/kailas-cloud/vecdex/internal/db"
	domcol "github.com/kailas-cloud/vecdex/internal/domain/collection"
	"github.com/kailas-cloud/vecdex/internal/domain/collection/field"
)

const testVectorDim = 1024

// mockStore implements the consumer interface for tests.
type mockStore struct {
	hsetFn             func(ctx context.Context, key string, fields map[string]string) error
	hgetAllFn          func(ctx context.Context, key string) (map[string]string, error)
	hgetAllMultiFn     func(ctx context.Context, keys []string) ([]map[string]string, error)
	delFn              func(ctx context.Context, key string) error
	existsFn           func(ctx context.Context, key string) (bool, error)
	scanFn             func(ctx context.Context, pattern string) ([]string, error)
	createIndexFn      func(ctx context.Context, def *db.IndexDefinition) error
	dropIndexFn        func(ctx context.Context, name string) error
	indexExistsFn      func(ctx context.Context, name string) (bool, error)
	supportsTextSearch bool
}

func (m *mockStore) HSet(ctx context.Context, key string, fields map[string]string) error {
	if m.hsetFn != nil {
		return m.hsetFn(ctx, key, fields)
	}
	return nil
}

func (m *mockStore) HGetAll(ctx context.Context, key string) (map[string]string, error) {
	if m.hgetAllFn != nil {
		return m.hgetAllFn(ctx, key)
	}
	return map[string]string{}, nil
}

func (m *mockStore) HGetAllMulti(ctx context.Context, keys []string) ([]map[string]string, error) {
	if m.hgetAllMultiFn != nil {
		return m.hgetAllMultiFn(ctx, keys)
	}
	return nil, nil
}

func (m *mockStore) Del(ctx context.Context, key string) error {
	if m.delFn != nil {
		return m.delFn(ctx, key)
	}
	return nil
}

func (m *mockStore) Exists(ctx context.Context, key string) (bool, error) {
	if m.existsFn != nil {
		return m.existsFn(ctx, key)
	}
	return false, nil
}

func (m *mockStore) Scan(ctx context.Context, pattern string) ([]string, error) {
	if m.scanFn != nil {
		return m.scanFn(ctx, pattern)
	}
	return nil, nil
}

func (m *mockStore) CreateIndex(ctx context.Context, def *db.IndexDefinition) error {
	if m.createIndexFn != nil {
		return m.createIndexFn(ctx, def)
	}
	return nil
}

func (m *mockStore) DropIndex(ctx context.Context, name string) error {
	if m.dropIndexFn != nil {
		return m.dropIndexFn(ctx, name)
	}
	return nil
}

func (m *mockStore) IndexExists(ctx context.Context, name string) (bool, error) {
	if m.indexExistsFn != nil {
		return m.indexExistsFn(ctx, name)
	}
	return false, nil
}

func (m *mockStore) SupportsTextSearch(_ context.Context) bool {
	return m.supportsTextSearch
}

func newTestRepo(t *testing.T) (*Repo, *mockStore) {
	t.Helper()
	ms := &mockStore{}
	repo := New(ms, testVectorDim)
	return repo, ms
}

func testCollection(t *testing.T) domcol.Collection {
	t.Helper()
	return domcol.Reconstruct(
		"test-collection",
		[]field.Field{
			field.Reconstruct("language", field.Tag),
			field.Reconstruct("priority", field.Numeric),
		},
		1024,
		1700000000000,
		1,
	)
}
