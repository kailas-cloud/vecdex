package search

import (
	"context"
	"slices"
	"testing"

	"github.com/kailas-cloud/vecdex/internal/db"
	"github.com/kailas-cloud/vecdex/internal/domain/search/filter"
)

func TestSearchMetadataComesFromTheCommittedParent(t *testing.T) {
	for _, raw := range []string{`{"nested":{"large":9007199254740993}}`, `{broken`} {
		t.Run(raw, func(t *testing.T) {
			repo, store := newTestRepo(t)
			store.searchKNNFn = func(_ context.Context, _ *db.KNNQuery) (*db.SearchResult, error) {
				return &db.SearchResult{Total: 2, Entries: []db.SearchEntry{
					{Key: "old", Fields: map[string]string{"__parent_id": "source", "__version": "old"}},
					{Key: "current", Fields: map[string]string{"__parent_id": "source", "__version": "current"}},
				}}, nil
			}
			store.projectionFn = func(_ context.Context, keys, fields []string) ([][]string, error) {
				if len(keys) != 1 || !slices.Equal(fields,
					[]string{"__chunk_version", "__source_metadata", "__source_version", "__expires_at"}) {
					t.Fatalf("expected one atomic parent projection: %v %v", keys, fields)
				}
				return [][]string{{"current", raw, "opaque-token", "0"}}, nil
			}
			found, err := repo.SearchKNN(testContext(), "notes", testVector(), filter.Expression{}, 10, false)
			if raw == `{broken` {
				if err == nil || len(found) != 0 {
					t.Fatal("corrupt parent metadata became a successful search")
				}
				return
			}
			if err != nil || len(found) != 1 || found[0].Version() != "opaque-token" ||
				string(found[0].Metadata().JSON()) != raw {
				t.Fatalf("source projection lost metadata or exposed an old fragment: %+v %v", found, err)
			}
		})
	}
}

func TestSearchMissingCommittedSourceVersionFailsWholeQuery(t *testing.T) {
	repo, store := newTestRepo(t)
	store.searchKNNFn = func(_ context.Context, _ *db.KNNQuery) (*db.SearchResult, error) {
		return &db.SearchResult{Total: 2, Entries: []db.SearchEntry{
			{Key: "valid", Fields: map[string]string{"__parent_id": "valid", "__version": "current"}},
			{Key: "invalid", Fields: map[string]string{"__parent_id": "invalid", "__version": "current"}},
		}}, nil
	}
	store.projectionFn = func(_ context.Context, _, _ []string) ([][]string, error) {
		return [][]string{{"current", `{}`, "valid-token", "0"}, {"current", `{}`, "", "0"}}, nil
	}
	found, err := repo.SearchKNN(testContext(), "notes", testVector(), filter.Expression{}, 10, false)
	if err == nil || len(found) != 0 {
		t.Fatal("a missing version became a successful search with unusable CAS tokens")
	}
}
