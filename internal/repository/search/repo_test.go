package search

import (
	"context"
	"encoding/binary"
	"errors"
	"math"
	"testing"

	"github.com/kailas-cloud/vecdex/internal/db"
	"github.com/kailas-cloud/vecdex/internal/domain/search/filter"
)

// --- SearchKNN ---

func TestSearchKNN_HappyPath(t *testing.T) {
	repo, ms := newTestRepo(t)
	ctx := context.Background()

	ms.searchKNNFn = func(_ context.Context, q *db.KNNQuery) (*db.SearchResult, error) {
		if q.IndexName != "vecdex:notes:idx" {
			t.Errorf("unexpected index: %s", q.IndexName)
		}
		if q.K != 10 {
			t.Errorf("unexpected K: %d", q.K)
		}
		return &db.SearchResult{
			Total: 2,
			Entries: []db.SearchEntry{
				{
					Key:   "vecdex:notes:doc-1",
					Score: 0.877,
					Fields: map[string]string{
						"__content": "hello world",
						"language":  "go",
						"priority":  "1.5",
					},
				},
				{
					Key:   "vecdex:notes:doc-2",
					Score: 0.544,
					Fields: map[string]string{
						"__content": "goodbye world",
						"language":  "rust",
					},
				},
			},
		}, nil
	}

	results, err := repo.SearchKNN(ctx, "notes", testVector(), filter.Expression{}, 10, false, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	if results[0].ID() != "doc-1" {
		t.Fatalf("expected ID doc-1, got %s", results[0].ID())
	}
	// Score comes from entry.Score set by db layer (cosine similarity 0.877)
	if results[0].Score() != 0.877 {
		t.Fatalf("expected score 0.877, got %f", results[0].Score())
	}
	if results[0].Content() != "hello world" {
		t.Fatalf("expected content 'hello world', got %s", results[0].Content())
	}
}

func TestSearchKNN_IncludeVectors(t *testing.T) {
	repo, ms := newTestRepo(t)
	ctx := context.Background()

	vec := []float32{0.1, 0.2, 0.3}
	vecBytes := testVectorToBytes(vec)

	ms.searchKNNFn = func(_ context.Context, q *db.KNNQuery) (*db.SearchResult, error) {
		if !q.IncludeVector {
			t.Error("expected IncludeVector=true")
		}
		return &db.SearchResult{
			Total: 1,
			Entries: []db.SearchEntry{
				{
					Key:   "vecdex:notes:doc-1",
					Score: 0.9,
					Fields: map[string]string{
						"__content": "text",
						"__vector":  vecBytes,
					},
				},
			},
		}, nil
	}

	results, err := repo.SearchKNN(ctx, "notes", testVector(), filter.Expression{}, 10, true, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if len(results[0].Vector()) != 3 {
		t.Fatalf("expected vector len 3, got %d", len(results[0].Vector()))
	}
}

func TestSearchKNN_EmptyResults(t *testing.T) {
	repo, ms := newTestRepo(t)
	ctx := context.Background()

	ms.searchKNNFn = func(_ context.Context, _ *db.KNNQuery) (*db.SearchResult, error) {
		return &db.SearchResult{Total: 0}, nil
	}

	results, err := repo.SearchKNN(ctx, "notes", testVector(), filter.Expression{}, 10, false, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("expected 0 results, got %d", len(results))
	}
}

func TestSearchKNN_Error(t *testing.T) {
	repo, ms := newTestRepo(t)
	ctx := context.Background()

	ms.searchKNNFn = func(_ context.Context, _ *db.KNNQuery) (*db.SearchResult, error) {
		return nil, errors.New("index not found")
	}

	_, err := repo.SearchKNN(ctx, "notes", testVector(), filter.Expression{}, 10, false, false)
	if err == nil {
		t.Fatal("expected error on SearchKNN failure")
	}
}

func TestSearchKNN_WithFilter(t *testing.T) {
	repo, ms := newTestRepo(t)
	ctx := context.Background()

	expr := mustExpression(t,
		[]filter.Condition{mustMatch(t, "language", "go")},
		nil, nil,
	)

	ms.searchKNNFn = func(_ context.Context, q *db.KNNQuery) (*db.SearchResult, error) {
		// Verify filter is passed through
		if q.Filters.IsEmpty() {
			t.Error("expected non-empty filters")
		}
		return &db.SearchResult{
			Total: 1,
			Entries: []db.SearchEntry{
				{
					Key:   "vecdex:notes:doc-1",
					Score: 0.9,
					Fields: map[string]string{
						"__content": "filtered",
						"language":  "go",
					},
				},
			},
		}, nil
	}

	results, err := repo.SearchKNN(ctx, "notes", testVector(), expr, 10, false, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
}

// --- SearchBM25 ---

func TestSearchBM25_HappyPath(t *testing.T) {
	repo, ms := newTestRepo(t)
	ctx := context.Background()

	ms.searchBM25Fn = func(_ context.Context, q *db.TextQuery) (*db.SearchResult, error) {
		if q.IndexName != "vecdex:notes:idx" {
			t.Errorf("unexpected index: %s", q.IndexName)
		}
		if q.Query != "hello" {
			t.Errorf("unexpected query: %s", q.Query)
		}
		return &db.SearchResult{
			Total: 2,
			Entries: []db.SearchEntry{
				{
					Key:   "vecdex:notes:doc-1",
					Score: 0.85,
					Fields: map[string]string{
						"__content": "hello world",
						"language":  "go",
					},
				},
				{
					Key:   "vecdex:notes:doc-2",
					Score: 0.42,
					Fields: map[string]string{
						"__content": "goodbye world",
						"language":  "rust",
					},
				},
			},
		}, nil
	}

	results, err := repo.SearchBM25(ctx, "notes", "hello", filter.Expression{}, 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	if results[0].ID() != "doc-1" {
		t.Fatalf("expected ID doc-1, got %s", results[0].ID())
	}
	if results[0].Score() != 0.85 {
		t.Fatalf("expected score 0.85, got %f", results[0].Score())
	}
	if results[0].Content() != "hello world" {
		t.Fatalf("expected content 'hello world', got %s", results[0].Content())
	}
}

func TestSearchBM25_EmptyResults(t *testing.T) {
	repo, ms := newTestRepo(t)
	ctx := context.Background()

	ms.searchBM25Fn = func(_ context.Context, _ *db.TextQuery) (*db.SearchResult, error) {
		return &db.SearchResult{Total: 0}, nil
	}

	results, err := repo.SearchBM25(ctx, "notes", "nothing", filter.Expression{}, 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("expected 0 results, got %d", len(results))
	}
}

func TestSearchBM25_Error(t *testing.T) {
	repo, ms := newTestRepo(t)
	ctx := context.Background()

	ms.searchBM25Fn = func(_ context.Context, _ *db.TextQuery) (*db.SearchResult, error) {
		return nil, errors.New("index not found")
	}

	_, err := repo.SearchBM25(ctx, "notes", "test", filter.Expression{}, 10)
	if err == nil {
		t.Fatal("expected error on SearchBM25 failure")
	}
}

// --- SupportsTextSearch ---

func TestSupportsTextSearch(t *testing.T) {
	repo, ms := newTestRepo(t)
	ctx := context.Background()

	ms.supportsTextSearchFn = func(_ context.Context) bool { return true }

	if !repo.SupportsTextSearch(ctx) {
		t.Fatal("expected SupportsTextSearch=true")
	}
}

// testVectorToBytes is a helper for tests.
func testVectorToBytes(v []float32) string {
	buf := make([]byte, len(v)*4)
	for i, f := range v {
		binary.LittleEndian.PutUint32(buf[i*4:], math.Float32bits(f))
	}
	return string(buf)
}
