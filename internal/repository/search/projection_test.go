package search

import (
	"context"
	"slices"
	"testing"

	"github.com/kailas-cloud/vecdex/internal/db"
	"github.com/kailas-cloud/vecdex/internal/domain"
	"github.com/kailas-cloud/vecdex/internal/domain/search/filter"
)

func TestSearchProjectionExcludesUnrequestedVectors(t *testing.T) {
	repo, store := newTestRepo(t)
	ctx := domain.WithSearchMetadataFields(context.Background(), []string{"language", "__n:priority"})
	for _, include := range []bool{false, true} {
		store.searchKNNFn = func(_ context.Context, q *db.KNNQuery) (*db.SearchResult, error) {
			if slices.Contains(q.ReturnFields, "__vector") != include {
				t.Fatalf("wrong vector projection: %v", q.ReturnFields)
			}
			for _, required := range []string{"language", "__n:priority", "__parent_id", "__version", "__vector_score"} {
				if !slices.Contains(q.ReturnFields, required) {
					t.Fatalf("missing %s: %v", required, q.ReturnFields)
				}
			}
			return &db.SearchResult{}, nil
		}
		if _, err := repo.SearchKNN(ctx, "notes", testVector(), filter.Expression{}, 10, include); err != nil {
			t.Fatal(err)
		}
	}
	store.searchBM25Fn = func(_ context.Context, q *db.TextQuery) (*db.SearchResult, error) {
		if slices.Contains(q.ReturnFields, "__vector") || !slices.Contains(q.ReturnFields, "language") {
			t.Fatalf("wrong keyword projection: %v", q.ReturnFields)
		}
		return &db.SearchResult{}, nil
	}
	if _, err := repo.SearchBM25(ctx, "notes", "text", filter.Expression{}, 10); err != nil {
		t.Fatal(err)
	}
}
