package document

import (
	"context"
	"testing"

	"github.com/kailas-cloud/vecdex/internal/db"
	domdoc "github.com/kailas-cloud/vecdex/internal/domain/document"
)

// mockStore implements the consumer interface for tests.
type mockStore struct {
	catalogIDsFn   func(context.Context, string, string, int) ([]string, error)
	catalogCountFn func(context.Context, string) (int, error)
	hgetAllMultiFn func(context.Context, []string) ([]map[string]string, error)
	mutateFn       func(context.Context, *db.HashMutation) (db.HashMutationResult, error)
	hsetFn         func(ctx context.Context, key string, fields map[string]string) error
	hsetMultiFn    func(ctx context.Context, items []db.HashSetItem) error
	hgetAllFn      func(ctx context.Context, key string) (map[string]string, error)
	hdelFn         func(ctx context.Context, key string, fields ...string) error
	delFn          func(ctx context.Context, key string) error
	existsFn       func(ctx context.Context, key string) (bool, error)
	searchListFn   func(
		ctx context.Context, index, query string, offset, limit int, fields []string,
	) (*db.SearchResult, error)
	searchCountFn func(ctx context.Context, index, query string) (int, error)
}

func (m *mockStore) HSet(ctx context.Context, key string, fields map[string]string) error {
	if m.hsetFn != nil {
		return m.hsetFn(ctx, key, fields)
	}
	return nil
}

func (m *mockStore) HSetMulti(ctx context.Context, items []db.HashSetItem) error {
	if m.hsetMultiFn != nil {
		return m.hsetMultiFn(ctx, items)
	}
	return nil
}

func (m *mockStore) HGetAll(ctx context.Context, key string) (map[string]string, error) {
	if m.hgetAllFn != nil {
		return m.hgetAllFn(ctx, key)
	}
	return map[string]string{}, nil
}

func (m *mockStore) HDel(ctx context.Context, key string, fields ...string) error {
	if m.hdelFn != nil {
		return m.hdelFn(ctx, key, fields...)
	}
	return nil
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

func (m *mockStore) SearchList(
	ctx context.Context, index, query string, offset, limit int, fields []string,
) (*db.SearchResult, error) {
	if m.searchListFn != nil {
		return m.searchListFn(ctx, index, query, offset, limit, fields)
	}
	return &db.SearchResult{}, nil
}

func (m *mockStore) SearchCount(ctx context.Context, index, query string) (int, error) {
	if m.searchCountFn != nil {
		return m.searchCountFn(ctx, index, query)
	}
	return 0, nil
}

func newTestRepo(t *testing.T) (*Repo, *mockStore) {
	t.Helper()
	ms := &mockStore{}
	repo := New(ms)
	return repo, ms
}

func testDocument(t *testing.T) domdoc.Document {
	t.Helper()
	vec := testVector(1024)
	return domdoc.Reconstruct("doc-1", "hello world",
		map[string]string{"language": "go"},
		map[string]float64{"priority": 1.5},
		vec, 1,
	)
}

func testVector(dim int) []float32 {
	vec := make([]float32, dim)
	for i := range vec {
		vec[i] = float32(i) * 0.001
	}
	return vec
}

func (m *mockStore) MutateHash(ctx context.Context, mutation *db.HashMutation) (db.HashMutationResult, error) {
	if m.mutateFn != nil {
		return m.mutateFn(ctx, mutation)
	}
	return db.HashMutationResult{Revision: 1, Created: true}, nil
}

func (m *mockStore) CatalogIDs(ctx context.Context, key, after string, limit int) ([]string, error) {
	if m.catalogIDsFn != nil {
		return m.catalogIDsFn(ctx, key, after, limit)
	}
	return nil, nil
}
func (m *mockStore) CatalogCount(ctx context.Context, key string) (int, error) {
	if m.catalogCountFn != nil {
		return m.catalogCountFn(ctx, key)
	}
	return 0, nil
}
func (m *mockStore) HGetAllMulti(ctx context.Context, keys []string) ([]map[string]string, error) {
	if m.hgetAllMultiFn != nil {
		return m.hgetAllMultiFn(ctx, keys)
	}
	rows := make([]map[string]string, len(keys))
	for i, key := range keys {
		row, err := m.HGetAll(ctx, key)
		if err != nil {
			return nil, err
		}
		rows[i] = row
	}
	return rows, nil
}
