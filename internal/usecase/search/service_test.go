package search

import (
	"context"
	"errors"
	"testing"

	"github.com/kailas-cloud/vecdex/internal/domain"
	domcol "github.com/kailas-cloud/vecdex/internal/domain/collection"
	"github.com/kailas-cloud/vecdex/internal/domain/collection/field"
	domdoc "github.com/kailas-cloud/vecdex/internal/domain/document"
	"github.com/kailas-cloud/vecdex/internal/domain/search/filter"
	"github.com/kailas-cloud/vecdex/internal/domain/search/mode"
	"github.com/kailas-cloud/vecdex/internal/domain/search/request"
	"github.com/kailas-cloud/vecdex/internal/domain/search/result"
)

// --- Mocks ---

type mockRepo struct {
	knnResults     []result.Result
	knnErr         error
	bm25Results    []result.Result
	bm25Err        error
	textSearchOK   bool
	knnCalled      bool
	bm25Called     bool
	lastIncludeVec bool
}

func (m *mockRepo) SearchKNN(
	_ context.Context, _ string,
	_ []float32, _ filter.Expression, _ int,
	includeVectors bool, _ bool,
) ([]result.Result, error) {
	m.knnCalled = true
	m.lastIncludeVec = includeVectors
	return m.knnResults, m.knnErr
}

func (m *mockRepo) SearchBM25(
	_ context.Context, _ string,
	_ string, _ filter.Expression, _ int,
) ([]result.Result, error) {
	m.bm25Called = true
	return m.bm25Results, m.bm25Err
}

func (m *mockRepo) SupportsTextSearch(_ context.Context) bool {
	return m.textSearchOK
}

type mockColls struct {
	col domcol.Collection
	err error
}

func (m *mockColls) Get(_ context.Context, _ string) (domcol.Collection, error) {
	return m.col, m.err
}

func defaultMockColls() *mockColls {
	return &mockColls{}
}

func mockCollsWithFields() *mockColls {
	tagField := field.Reconstruct("category", field.Tag)
	numField := field.Reconstruct("price", field.Numeric)
	col := domcol.Reconstruct("test-col", domcol.TypeText, []field.Field{tagField, numField}, 128, 0, 1)
	return &mockColls{col: col}
}

type mockEmbedder struct {
	vec    []float32
	err    error
	called bool
}

func (m *mockEmbedder) Embed(_ context.Context, _ string) (domain.EmbeddingResult, error) {
	m.called = true
	if m.err != nil {
		return domain.EmbeddingResult{}, m.err
	}
	return domain.EmbeddingResult{Embedding: m.vec}, nil
}

func makeSearchRequest(t *testing.T, m mode.Mode) *request.Request {
	t.Helper()
	r, err := request.New("test query", m, filter.Expression{}, 10, 10, 0, false, nil)
	if err != nil {
		t.Fatalf("request.New: %v", err)
	}
	return &r
}

// --- Tests ---

func TestSearch_Semantic(t *testing.T) {
	repo := &mockRepo{
		knnResults:   []result.Result{result.New("a", 0.9, "text", nil, nil, nil)},
		textSearchOK: true,
	}
	embed := &mockEmbedder{vec: []float32{0.1, 0.2}}
	svc := New(repo, defaultMockColls(), embed)

	req := makeSearchRequest(t, mode.Semantic)
	results, _, err := svc.Search(context.Background(), "test-col", req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if !repo.knnCalled {
		t.Error("expected SearchKNN to be called")
	}
	if repo.bm25Called {
		t.Error("SearchBM25 should not be called in semantic mode")
	}
	if !embed.called {
		t.Error("expected Embed to be called")
	}
}

func TestSearch_Keyword(t *testing.T) {
	repo := &mockRepo{
		bm25Results:  []result.Result{result.New("a", 0.8, "text", nil, nil, nil)},
		textSearchOK: true,
	}
	embed := &mockEmbedder{vec: []float32{0.1}}
	svc := New(repo, defaultMockColls(), embed)

	req := makeSearchRequest(t, mode.Keyword)
	results, _, err := svc.Search(context.Background(), "test-col", req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if repo.knnCalled {
		t.Error("SearchKNN should not be called in keyword mode")
	}
	if !repo.bm25Called {
		t.Error("expected SearchBM25 to be called")
	}
	if embed.called {
		t.Error("Embed should not be called in keyword mode")
	}
}

func TestSearch_Hybrid(t *testing.T) {
	repo := &mockRepo{
		knnResults:   []result.Result{result.New("a", 0.9, "text", nil, nil, nil)},
		bm25Results:  []result.Result{result.New("b", 0.8, "text", nil, nil, nil)},
		textSearchOK: true,
	}
	embed := &mockEmbedder{vec: []float32{0.1}}
	svc := New(repo, defaultMockColls(), embed)

	req := makeSearchRequest(t, mode.Hybrid)
	results, _, err := svc.Search(context.Background(), "test-col", req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	if !repo.knnCalled {
		t.Error("expected SearchKNN to be called")
	}
	if !repo.bm25Called {
		t.Error("expected SearchBM25 to be called")
	}
	if !embed.called {
		t.Error("expected Embed to be called")
	}
}

func TestSearch_KeywordOnValkey_ReturnsError(t *testing.T) {
	repo := &mockRepo{textSearchOK: false}
	embed := &mockEmbedder{vec: []float32{0.1}}
	svc := New(repo, defaultMockColls(), embed)

	req := makeSearchRequest(t, mode.Keyword)
	_, _, err := svc.Search(context.Background(), "test-col", req)
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, domain.ErrKeywordSearchNotSupported) {
		t.Errorf("expected ErrKeywordSearchNotSupported, got %v", err)
	}
}

func TestSearch_HybridOnValkey_ReturnsError(t *testing.T) {
	repo := &mockRepo{textSearchOK: false}
	embed := &mockEmbedder{vec: []float32{0.1}}
	svc := New(repo, defaultMockColls(), embed)

	req := makeSearchRequest(t, mode.Hybrid)
	_, _, err := svc.Search(context.Background(), "test-col", req)
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, domain.ErrKeywordSearchNotSupported) {
		t.Errorf("expected ErrKeywordSearchNotSupported, got %v", err)
	}
}

func TestSearch_SemanticOnValkey_Works(t *testing.T) {
	repo := &mockRepo{
		knnResults:   []result.Result{result.New("a", 0.9, "text", nil, nil, nil)},
		textSearchOK: false, // Valkey backend
	}
	embed := &mockEmbedder{vec: []float32{0.1}}
	svc := New(repo, defaultMockColls(), embed)

	req := makeSearchRequest(t, mode.Semantic)
	results, _, err := svc.Search(context.Background(), "test-col", req)
	if err != nil {
		t.Fatalf("semantic should work on Valkey, got error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
}

func TestSearch_MinScoreFilter(t *testing.T) {
	repo := &mockRepo{
		knnResults: []result.Result{
			result.New("a", 0.9, "high", nil, nil, nil),
			result.New("b", 0.3, "low", nil, nil, nil),
		},
		textSearchOK: true,
	}
	embed := &mockEmbedder{vec: []float32{0.1}}
	svc := New(repo, defaultMockColls(), embed)

	r, _ := request.New("test", mode.Semantic, filter.Expression{}, 10, 10, 0.5, false, nil)
	results, _, err := svc.Search(context.Background(), "test-col", &r)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result after min_score filter, got %d", len(results))
	}
	if results[0].ID() != "a" {
		t.Errorf("expected 'a', got %s", results[0].ID())
	}
}

// --- Filter validation tests ---

func TestSearch_FilterValidation_UnknownField(t *testing.T) {
	repo := &mockRepo{textSearchOK: true}
	embed := &mockEmbedder{vec: []float32{0.1}}
	svc := New(repo, mockCollsWithFields(), embed)

	matchCond, _ := filter.NewMatch("nonexistent", "val")
	expr, _ := filter.NewExpression([]filter.Condition{matchCond}, nil, nil)
	r, _ := request.New("test", mode.Semantic, expr, 10, 10, 0, false, nil)

	_, _, err := svc.Search(context.Background(), "test-col", &r)
	if err == nil {
		t.Fatal("expected error for unknown field")
	}
	if !errors.Is(err, domain.ErrInvalidSchema) {
		t.Errorf("expected ErrInvalidSchema, got %v", err)
	}
}

func TestSearch_FilterValidation_MatchOnNumeric(t *testing.T) {
	repo := &mockRepo{textSearchOK: true}
	embed := &mockEmbedder{vec: []float32{0.1}}
	svc := New(repo, mockCollsWithFields(), embed)

	matchCond, _ := filter.NewMatch("price", "100")
	expr, _ := filter.NewExpression([]filter.Condition{matchCond}, nil, nil)
	r, _ := request.New("test", mode.Semantic, expr, 10, 10, 0, false, nil)

	_, _, err := svc.Search(context.Background(), "test-col", &r)
	if err == nil {
		t.Fatal("expected error for match on numeric field")
	}
	if !errors.Is(err, domain.ErrInvalidSchema) {
		t.Errorf("expected ErrInvalidSchema, got %v", err)
	}
}

func TestSearch_FilterValidation_RangeOnTag(t *testing.T) {
	repo := &mockRepo{textSearchOK: true}
	embed := &mockEmbedder{vec: []float32{0.1}}
	svc := New(repo, mockCollsWithFields(), embed)

	v := 10.0
	rng, _ := filter.NewRangeFilter(&v, nil, nil, nil)
	rangeCond, _ := filter.NewRange("category", rng)
	expr, _ := filter.NewExpression([]filter.Condition{rangeCond}, nil, nil)
	r, _ := request.New("test", mode.Semantic, expr, 10, 10, 0, false, nil)

	_, _, err := svc.Search(context.Background(), "test-col", &r)
	if err == nil {
		t.Fatal("expected error for range on tag field")
	}
	if !errors.Is(err, domain.ErrInvalidSchema) {
		t.Errorf("expected ErrInvalidSchema, got %v", err)
	}
}

func TestSearch_FilterValidation_ValidMatch(t *testing.T) {
	repo := &mockRepo{
		knnResults:   []result.Result{result.New("a", 0.9, "text", nil, nil, nil)},
		textSearchOK: true,
	}
	embed := &mockEmbedder{vec: []float32{0.1}}
	svc := New(repo, mockCollsWithFields(), embed)

	matchCond, _ := filter.NewMatch("category", "electronics")
	expr, _ := filter.NewExpression([]filter.Condition{matchCond}, nil, nil)
	r, _ := request.New("test", mode.Semantic, expr, 10, 10, 0, false, nil)

	results, _, err := svc.Search(context.Background(), "test-col", &r)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
}

func TestSearch_FilterValidation_ValidRange(t *testing.T) {
	repo := &mockRepo{
		knnResults:   []result.Result{result.New("a", 0.9, "text", nil, nil, nil)},
		textSearchOK: true,
	}
	embed := &mockEmbedder{vec: []float32{0.1}}
	svc := New(repo, mockCollsWithFields(), embed)

	v := 50.0
	rng, _ := filter.NewRangeFilter(nil, &v, nil, nil)
	rangeCond, _ := filter.NewRange("price", rng)
	expr, _ := filter.NewExpression([]filter.Condition{rangeCond}, nil, nil)
	r, _ := request.New("test", mode.Semantic, expr, 10, 10, 0, false, nil)

	results, _, err := svc.Search(context.Background(), "test-col", &r)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
}

func TestSearch_CollectionNotFound(t *testing.T) {
	repo := &mockRepo{textSearchOK: true}
	embed := &mockEmbedder{vec: []float32{0.1}}
	colls := &mockColls{err: domain.ErrNotFound}
	svc := New(repo, colls, embed)

	r, _ := request.New("test", mode.Semantic, filter.Expression{}, 10, 10, 0, false, nil)
	_, _, err := svc.Search(context.Background(), "missing", &r)
	if err == nil {
		t.Fatal("expected error for missing collection")
	}
	if !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestSearch_EmbedError(t *testing.T) {
	repo := &mockRepo{textSearchOK: true}
	embed := &mockEmbedder{err: errors.New("embedding provider down")}
	svc := New(repo, defaultMockColls(), embed)

	r, _ := request.New("test", mode.Semantic, filter.Expression{}, 10, 10, 0, false, nil)
	_, _, err := svc.Search(context.Background(), "test-col", &r)
	if err == nil {
		t.Fatal("expected error from embedding failure")
	}
}

func TestSearch_IncludeVectors(t *testing.T) {
	repo := &mockRepo{
		knnResults:   []result.Result{result.New("a", 0.9, "text", nil, nil, nil)},
		textSearchOK: true,
	}
	embed := &mockEmbedder{vec: []float32{0.1}}
	svc := New(repo, defaultMockColls(), embed)

	r, _ := request.New("test", mode.Semantic, filter.Expression{}, 10, 10, 0, true, nil)
	_, _, err := svc.Search(context.Background(), "test-col", &r)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !repo.lastIncludeVec {
		t.Error("expected includeVectors=true to be passed to repo")
	}
}

// --- Document reader mock ---

type mockDocs struct {
	doc domdoc.Document
	err error
}

func (m *mockDocs) Get(_ context.Context, _, _ string) (domdoc.Document, error) {
	return m.doc, m.err
}

func makeSimilarRequest(t *testing.T) *request.SimilarRequest {
	t.Helper()
	r, err := request.NewSimilar(filter.Expression{}, 10, 10, 0, false)
	if err != nil {
		t.Fatalf("request.NewSimilar: %v", err)
	}
	return &r
}

func mockGeoColls() *mockColls {
	col := domcol.Reconstruct("geo-col", domcol.TypeGeo, nil, 3, 0, 1)
	return &mockColls{col: col}
}

// --- Similar tests ---

func TestSimilar_HappyPath(t *testing.T) {
	repo := &mockRepo{
		knnResults: []result.Result{
			result.New("source", 1.0, "source doc", nil, nil, nil),
			result.New("similar-1", 0.9, "similar doc", nil, nil, nil),
			result.New("similar-2", 0.7, "another doc", nil, nil, nil),
		},
	}
	docs := &mockDocs{
		doc: domdoc.Reconstruct("source", "source content", nil, nil, []float32{0.1, 0.2, 0.3}, 1),
	}
	embed := &mockEmbedder{vec: []float32{0.1}}
	svc := New(repo, defaultMockColls(), embed).WithDocuments(docs)

	req := makeSimilarRequest(t)
	results, total, err := svc.Similar(context.Background(), "test-col", "source", req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if total != 2 {
		t.Errorf("expected total=2 (source excluded), got %d", total)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	for _, r := range results {
		if r.ID() == "source" {
			t.Error("source document should be excluded from results")
		}
	}
	if embed.called {
		t.Error("embedder should NOT be called — similar reuses stored vector")
	}
	if !repo.knnCalled {
		t.Error("expected SearchKNN to be called")
	}
}

func TestSimilar_CollectionNotFound(t *testing.T) {
	repo := &mockRepo{}
	docs := &mockDocs{}
	embed := &mockEmbedder{}
	colls := &mockColls{err: domain.ErrNotFound}
	svc := New(repo, colls, embed).WithDocuments(docs)

	req := makeSimilarRequest(t)
	_, _, err := svc.Similar(context.Background(), "missing", "doc-1", req)
	if err == nil {
		t.Fatal("expected error for missing collection")
	}
	if !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestSimilar_GeoCollection_ReturnsError(t *testing.T) {
	repo := &mockRepo{}
	docs := &mockDocs{}
	embed := &mockEmbedder{}
	svc := New(repo, mockGeoColls(), embed).WithDocuments(docs)

	req := makeSimilarRequest(t)
	_, _, err := svc.Similar(context.Background(), "geo-col", "doc-1", req)
	if err == nil {
		t.Fatal("expected error for geo collection")
	}
	if !errors.Is(err, domain.ErrCollectionTypeMismatch) {
		t.Errorf("expected ErrCollectionTypeMismatch, got %v", err)
	}
}

func TestSimilar_DocumentNotFound(t *testing.T) {
	repo := &mockRepo{}
	docs := &mockDocs{err: domain.ErrDocumentNotFound}
	embed := &mockEmbedder{}
	svc := New(repo, defaultMockColls(), embed).WithDocuments(docs)

	req := makeSimilarRequest(t)
	_, _, err := svc.Similar(context.Background(), "test-col", "missing", req)
	if err == nil {
		t.Fatal("expected error for missing document")
	}
	if !errors.Is(err, domain.ErrDocumentNotFound) {
		t.Errorf("expected ErrDocumentNotFound, got %v", err)
	}
}

func TestSimilar_DocumentHasNoVector(t *testing.T) {
	repo := &mockRepo{}
	docs := &mockDocs{
		doc: domdoc.Reconstruct("no-vec", "content", nil, nil, nil, 1),
	}
	embed := &mockEmbedder{}
	svc := New(repo, defaultMockColls(), embed).WithDocuments(docs)

	req := makeSimilarRequest(t)
	_, _, err := svc.Similar(context.Background(), "test-col", "no-vec", req)
	if err == nil {
		t.Fatal("expected error for document without vector")
	}
	if !errors.Is(err, domain.ErrDocumentNotFound) {
		t.Errorf("expected ErrDocumentNotFound, got %v", err)
	}
}

func TestSimilar_KNNError(t *testing.T) {
	repo := &mockRepo{knnErr: errors.New("knn failure")}
	docs := &mockDocs{
		doc: domdoc.Reconstruct("doc-1", "content", nil, nil, []float32{0.1}, 1),
	}
	embed := &mockEmbedder{}
	svc := New(repo, defaultMockColls(), embed).WithDocuments(docs)

	req := makeSimilarRequest(t)
	_, _, err := svc.Similar(context.Background(), "test-col", "doc-1", req)
	if err == nil {
		t.Fatal("expected error from KNN failure")
	}
}

func TestSimilar_MinScoreFilter(t *testing.T) {
	repo := &mockRepo{
		knnResults: []result.Result{
			result.New("a", 0.95, "high", nil, nil, nil),
			result.New("b", 0.4, "low", nil, nil, nil),
		},
	}
	docs := &mockDocs{
		doc: domdoc.Reconstruct("source", "content", nil, nil, []float32{0.1}, 1),
	}
	embed := &mockEmbedder{}
	svc := New(repo, defaultMockColls(), embed).WithDocuments(docs)

	r, _ := request.NewSimilar(filter.Expression{}, 10, 10, 0.5, false)
	results, total, err := svc.Similar(context.Background(), "test-col", "source", &r)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if total != 1 {
		t.Errorf("expected total=1 after min_score, got %d", total)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].ID() != "a" {
		t.Errorf("expected 'a', got %s", results[0].ID())
	}
}

func TestSimilar_SourceExcluded_ResultsSorted(t *testing.T) {
	repo := &mockRepo{
		knnResults: []result.Result{
			result.New("source", 1.0, "self", nil, nil, nil),
			result.New("c", 0.5, "low", nil, nil, nil),
			result.New("a", 0.9, "high", nil, nil, nil),
			result.New("b", 0.7, "mid", nil, nil, nil),
		},
	}
	docs := &mockDocs{
		doc: domdoc.Reconstruct("source", "content", nil, nil, []float32{0.1}, 1),
	}
	embed := &mockEmbedder{}
	svc := New(repo, defaultMockColls(), embed).WithDocuments(docs)

	req := makeSimilarRequest(t)
	results, _, err := svc.Similar(context.Background(), "test-col", "source", req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}
	// Проверяем сортировку по убыванию score
	for i := 1; i < len(results); i++ {
		if results[i].Score() > results[i-1].Score() {
			t.Errorf("results not sorted descending: [%d]=%f > [%d]=%f",
				i, results[i].Score(), i-1, results[i-1].Score())
		}
	}
}

func TestSimilar_FilterValidation(t *testing.T) {
	repo := &mockRepo{}
	docs := &mockDocs{
		doc: domdoc.Reconstruct("doc-1", "content", nil, nil, []float32{0.1}, 1),
	}
	embed := &mockEmbedder{}
	svc := New(repo, mockCollsWithFields(), embed).WithDocuments(docs)

	matchCond, _ := filter.NewMatch("nonexistent", "val")
	expr, _ := filter.NewExpression([]filter.Condition{matchCond}, nil, nil)
	r, _ := request.NewSimilar(expr, 10, 10, 0, false)

	_, _, err := svc.Similar(context.Background(), "test-col", "doc-1", &r)
	if err == nil {
		t.Fatal("expected error for unknown filter field")
	}
	if !errors.Is(err, domain.ErrInvalidSchema) {
		t.Errorf("expected ErrInvalidSchema, got %v", err)
	}
}
