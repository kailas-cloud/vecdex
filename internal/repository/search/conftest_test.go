package search

import (
	"context"
	"testing"

	"github.com/kailas-cloud/vecdex/internal/db"
	"github.com/kailas-cloud/vecdex/internal/domain/search/filter"
)

// mockStore implements the consumer interface for tests.
type mockStore struct {
	searchKNNFn          func(ctx context.Context, q *db.KNNQuery) (*db.SearchResult, error)
	searchBM25Fn         func(ctx context.Context, q *db.TextQuery) (*db.SearchResult, error)
	supportsTextSearchFn func(ctx context.Context) bool
}

func (m *mockStore) SearchKNN(ctx context.Context, q *db.KNNQuery) (*db.SearchResult, error) {
	if m.searchKNNFn != nil {
		return m.searchKNNFn(ctx, q)
	}
	return &db.SearchResult{}, nil
}

func (m *mockStore) SearchBM25(ctx context.Context, q *db.TextQuery) (*db.SearchResult, error) {
	if m.searchBM25Fn != nil {
		return m.searchBM25Fn(ctx, q)
	}
	return &db.SearchResult{}, nil
}

func (m *mockStore) SupportsTextSearch(ctx context.Context) bool {
	if m.supportsTextSearchFn != nil {
		return m.supportsTextSearchFn(ctx)
	}
	return false
}

func newTestRepo(t *testing.T) (*Repo, *mockStore) {
	t.Helper()
	ms := &mockStore{}
	repo := New(ms)
	return repo, ms
}

func testVector() []float32 {
	vec := make([]float32, 4)
	for i := range vec {
		vec[i] = 0.1
	}
	return vec
}

func mustMatch(t *testing.T, key, value string) filter.Condition {
	t.Helper()
	c, err := filter.NewMatch(key, value)
	if err != nil {
		t.Fatalf("NewMatch: %v", err)
	}
	return c
}

func mustExpression(t *testing.T, must, should, mustNot []filter.Condition) filter.Expression {
	t.Helper()
	e, err := filter.NewExpression(must, should, mustNot)
	if err != nil {
		t.Fatalf("NewExpression: %v", err)
	}
	return e
}
