package batch

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/kailas-cloud/vecdex/internal/domain"
	dombatch "github.com/kailas-cloud/vecdex/internal/domain/batch"
	domcol "github.com/kailas-cloud/vecdex/internal/domain/collection"
	"github.com/kailas-cloud/vecdex/internal/domain/collection/field"
	domdoc "github.com/kailas-cloud/vecdex/internal/domain/document"
)

// --- Mocks ---

type mockDocUpserter struct {
	fn        func(*domdoc.Document) (bool, error)
	upsertErr error
	callCount int
}

func (m *mockDocUpserter) Upsert(_ context.Context, _ string, doc *domdoc.Document) (bool, error) {
	if m.fn != nil {
		return m.fn(doc)
	}
	m.callCount++
	return true, m.upsertErr
}

type mockBatchUpserter struct {
	err       error
	callCount int
}

func (m *mockBatchUpserter) BatchUpsert(_ context.Context, _ string, _ []domdoc.Document) error {
	m.callCount++
	return m.err
}

type mockDocDeleter struct {
	deleteErr error
	callCount int
	failOnID  string // fail only for this ID
}

func (m *mockDocDeleter) Delete(_ context.Context, _, id string) error {
	m.callCount++
	if m.failOnID != "" && id == m.failOnID {
		return m.deleteErr
	}
	if m.failOnID == "" {
		return m.deleteErr
	}
	return nil
}

type mockCollReader struct {
	col domcol.Collection
	err error
}

func (m *mockCollReader) Get(_ context.Context, _ string) (domcol.Collection, error) {
	return m.col, m.err
}

type mockEmbedder struct {
	observeContext func(context.Context)
	fn             func(string) (domain.EmbeddingResult, error)
	result         domain.EmbeddingResult
	err            error
	callCount      int
	failAfter      int // fail after N successful calls; 0=always succeed (unless err set)

	// Batch support
	batchResult domain.BatchEmbeddingResult
	batchErr    error
	batchCalls  int
}

func (m *mockEmbedder) Embed(_ context.Context, text string) (domain.EmbeddingResult, error) {
	if m.fn != nil {
		return m.fn(text)
	}
	m.callCount++
	if m.failAfter > 0 && m.callCount > m.failAfter {
		return domain.EmbeddingResult{}, m.err
	}
	if m.failAfter == 0 && m.err != nil {
		return domain.EmbeddingResult{}, m.err
	}
	return m.result, nil
}

func (m *mockEmbedder) BatchEmbed(ctx context.Context, texts []string) (domain.BatchEmbeddingResult, error) {
	if m.observeContext != nil {
		m.observeContext(ctx)
	}
	m.batchCalls++
	if m.batchErr != nil {
		return m.batchResult, m.batchErr
	}
	if m.batchResult.Embeddings != nil {
		return m.batchResult, nil
	}
	// Авто-генерация: вернуть по вектору из result.Embedding на каждый текст
	embeddings := make([][]float32, len(texts))
	for i := range texts {
		embeddings[i] = m.result.Embedding
	}
	return domain.BatchEmbeddingResult{
		Embeddings:   embeddings,
		PromptTokens: m.result.PromptTokens * len(texts),
		TotalTokens:  m.result.TotalTokens * len(texts),
	}, nil
}

func makeField(t *testing.T, name string, ft field.Type) field.Field {
	t.Helper()
	f, err := field.New(name, ft)
	if err != nil {
		t.Fatalf("field.New: %v", err)
	}
	return f
}

func makeCollection(t *testing.T, fields []field.Field) domcol.Collection {
	t.Helper()
	col, err := domcol.New("test-col", domcol.TypeText, fields, 3)
	if err != nil {
		t.Fatalf("domcol.New: %v", err)
	}
	return col
}

func makeDoc(t *testing.T, id string) domdoc.Document {
	t.Helper()
	doc, err := domdoc.New(id, "content for "+id, nil, nil)
	if err != nil {
		t.Fatalf("domdoc.New: %v", err)
	}
	return doc
}

func makeDocWithTags(t *testing.T, id string, tags map[string]string) domdoc.Document {
	t.Helper()
	doc, err := domdoc.New(id, "content", tags, nil)
	if err != nil {
		t.Fatalf("domdoc.New: %v", err)
	}
	return doc
}

// --- Upsert tests ---

func TestUpsert_Success(t *testing.T) {
	col := makeCollection(t, nil)
	docs := &mockDocUpserter{}
	del := &mockDocDeleter{}
	colls := &mockCollReader{col: col}
	embed := &mockEmbedder{result: domain.EmbeddingResult{Embedding: []float32{0.1, 0.2, 0.3}}}

	svc := New(docs, &mockBatchUpserter{}, del, colls, embed, embed)
	items := []domdoc.Document{makeDoc(t, "a"), makeDoc(t, "b"), makeDoc(t, "c")}
	results := svc.Upsert(context.Background(), "test-col", upsertItemsForTest(items))

	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}
	for i, r := range results {
		if r.Status() != dombatch.StatusOK {
			t.Errorf("result[%d] expected ok, got error: %v", i, r.Err())
		}
	}
}

func TestUpsert_PartialFailure(t *testing.T) {
	col := makeCollection(t, []field.Field{makeField(t, "cat", field.Tag)})
	docs := &mockDocUpserter{}
	del := &mockDocDeleter{}
	colls := &mockCollReader{col: col}
	embed := &mockEmbedder{result: domain.EmbeddingResult{Embedding: []float32{0.1, 0.2, 0.3}}}

	svc := New(docs, &mockBatchUpserter{}, del, colls, embed, embed)
	items := []domdoc.Document{
		makeDoc(t, "a"), // ok — no fields
		makeDocWithTags(t, "b", map[string]string{"unknown": "val"}), // ok — unknown tags allowed (stored)
		makeDoc(t, "c"), // ok
	}
	results := svc.Upsert(context.Background(), "test-col", upsertItemsForTest(items))

	for i, r := range results {
		if r.Status() != dombatch.StatusOK {
			t.Errorf("result[%d] expected ok, got %v", i, r.Err())
		}
	}
}

func TestUpsert_ExplicitFormerChunkFields(t *testing.T) {
	col := makeCollection(t, []field.Field{
		makeField(t, "parent_doc_id", field.Tag), makeField(t, "chunk_index", field.Numeric),
	})
	docs := &mockDocUpserter{}
	del := &mockDocDeleter{}
	colls := &mockCollReader{col: col}
	embed := &mockEmbedder{result: domain.EmbeddingResult{Embedding: []float32{0.1, 0.2, 0.3}}}

	svc := New(docs, &mockBatchUpserter{}, del, colls, embed, embed)
	item, err := domdoc.New("a", "content", map[string]string{
		"parent_doc_id": "a",
	}, map[string]float64{
		"chunk_index": 1,
	})
	if err != nil {
		t.Fatalf("domdoc.New: %v", err)
	}

	results := svc.Upsert(context.Background(), "test-col", upsertItemsForTest([]domdoc.Document{item}))
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Status() != dombatch.StatusOK {
		t.Fatalf("expected ok, got %v", results[0].Err())
	}
}

func TestUpsert_ExceedsMax(t *testing.T) {
	col := makeCollection(t, nil)
	docs := &mockDocUpserter{}
	del := &mockDocDeleter{}
	colls := &mockCollReader{col: col}
	embed := &mockEmbedder{result: domain.EmbeddingResult{Embedding: []float32{0.1}}}

	svc := New(docs, &mockBatchUpserter{}, del, colls, embed, embed)
	items := make([]domdoc.Document, MaxBatchSize+1)
	for i := range items {
		items[i] = makeDoc(t, fmt.Sprintf("doc-%d", i))
	}

	results := svc.Upsert(context.Background(), "test-col", upsertItemsForTest(items))
	for _, r := range results {
		if r.Status() != dombatch.StatusError {
			t.Error("expected all errors for oversized batch")
		}
	}
}

func TestUpsert_CollectionNotFound(t *testing.T) {
	docs := &mockDocUpserter{}
	del := &mockDocDeleter{}
	colls := &mockCollReader{err: domain.ErrNotFound}
	embed := &mockEmbedder{result: domain.EmbeddingResult{Embedding: []float32{0.1}}}

	svc := New(docs, &mockBatchUpserter{}, del, colls, embed, embed)
	items := []domdoc.Document{makeDoc(t, "a")}
	results := svc.Upsert(context.Background(), "nonexistent", upsertItemsForTest(items))

	if results[0].Status() != dombatch.StatusError {
		t.Error("expected error for nonexistent collection")
	}
	if !errors.Is(results[0].Err(), domain.ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", results[0].Err())
	}
}

func TestUpsert_QuotaCascade(t *testing.T) {
	col := makeCollection(t, nil)
	docs := &mockDocUpserter{}
	del := &mockDocDeleter{}
	colls := &mockCollReader{col: col}
	embed := &mockEmbedder{
		result:   domain.EmbeddingResult{Embedding: []float32{0.1, 0.2, 0.3}},
		batchErr: domain.ErrEmbeddingQuotaExceeded,
	}

	svc := New(docs, &mockBatchUpserter{}, del, colls, embed, embed)
	items := []domdoc.Document{makeDoc(t, "a"), makeDoc(t, "b"), makeDoc(t, "c")}
	results := svc.Upsert(context.Background(), "test-col", upsertItemsForTest(items))

	// Batch embed fails atomically — all items get the error
	for i, r := range results {
		if r.Status() != dombatch.StatusError {
			t.Errorf("result[%d] expected error", i)
		}
		if !errors.Is(r.Err(), domain.ErrEmbeddingQuotaExceeded) {
			t.Errorf("result[%d] expected ErrEmbeddingQuotaExceeded, got %v", i, r.Err())
		}
	}
}

func TestUpsert_RateLimitCascade(t *testing.T) {
	col := makeCollection(t, nil)
	docs := &mockDocUpserter{}
	del := &mockDocDeleter{}
	colls := &mockCollReader{col: col}
	embed := &mockEmbedder{
		result:   domain.EmbeddingResult{Embedding: []float32{0.1, 0.2, 0.3}},
		batchErr: domain.ErrRateLimited,
	}

	svc := New(docs, &mockBatchUpserter{}, del, colls, embed, embed)
	items := []domdoc.Document{makeDoc(t, "a"), makeDoc(t, "b")}
	results := svc.Upsert(context.Background(), "test-col", upsertItemsForTest(items))

	// Batch embed fails atomically — all items get rate limit error
	for i, r := range results {
		if r.Status() != dombatch.StatusError {
			t.Errorf("result[%d] expected error", i)
		}
		if !errors.Is(r.Err(), domain.ErrRateLimited) {
			t.Errorf("result[%d] expected ErrRateLimited, got %v", i, r.Err())
		}
	}
}

func TestUpsert_BatchEmbedError(t *testing.T) {
	col := makeCollection(t, nil)
	docs := &mockDocUpserter{}
	del := &mockDocDeleter{}
	colls := &mockCollReader{col: col}
	providerErr := errors.New("model unavailable")
	embed := &mockEmbedder{
		result:   domain.EmbeddingResult{Embedding: []float32{0.1, 0.2, 0.3}},
		batchErr: providerErr,
	}

	svc := New(docs, &mockBatchUpserter{}, del, colls, embed, embed)
	items := []domdoc.Document{makeDoc(t, "a"), makeDoc(t, "b"), makeDoc(t, "c")}
	results := svc.Upsert(context.Background(), "test-col", upsertItemsForTest(items))

	// Batch embed fails — all items get the error
	for i, r := range results {
		if r.Status() != dombatch.StatusError {
			t.Errorf("result[%d] expected error, got ok", i)
		}
	}
}

func TestUpsert_UnknownTagsAllowed(t *testing.T) {
	col := makeCollection(t, []field.Field{makeField(t, "lang", field.Tag)})
	docs := &mockDocUpserter{}
	del := &mockDocDeleter{}
	colls := &mockCollReader{col: col}
	embed := &mockEmbedder{result: domain.EmbeddingResult{Embedding: []float32{0.1, 0.2, 0.3}}}

	svc := New(docs, &mockBatchUpserter{}, del, colls, embed, embed)
	items := []domdoc.Document{
		makeDocWithTags(t, "a", map[string]string{"lang": "go"}),     // ok — known tag
		makeDocWithTags(t, "b", map[string]string{"unknown": "val"}), // ok — stored field
	}
	results := svc.Upsert(context.Background(), "test-col", upsertItemsForTest(items))

	for i, r := range results {
		if r.Status() != dombatch.StatusOK {
			t.Errorf("result[%d] expected ok, got %v", i, r.Err())
		}
	}
}

// --- Delete tests ---

func TestDelete_Success(t *testing.T) {
	col := makeCollection(t, nil)
	docs := &mockDocUpserter{}
	del := &mockDocDeleter{}
	colls := &mockCollReader{col: col}
	embed := &mockEmbedder{}

	svc := New(docs, &mockBatchUpserter{}, del, colls, embed, embed)
	results := svc.Delete(context.Background(), "test-col", deleteItemsForTest([]string{"a", "b"}))

	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	for _, r := range results {
		if r.Status() != dombatch.StatusOK {
			t.Errorf("expected ok, got %v", r.Err())
		}
	}
}

func TestDelete_PartialFailure(t *testing.T) {
	col := makeCollection(t, nil)
	docs := &mockDocUpserter{}
	del := &mockDocDeleter{deleteErr: domain.ErrDocumentNotFound, failOnID: "b"}
	colls := &mockCollReader{col: col}
	embed := &mockEmbedder{}

	svc := New(docs, &mockBatchUpserter{}, del, colls, embed, embed)
	results := svc.Delete(context.Background(), "test-col", deleteItemsForTest([]string{"a", "b", "c"}))

	if results[0].Status() != dombatch.StatusOK {
		t.Error("result[0] expected ok")
	}
	if results[1].Status() != dombatch.StatusError {
		t.Error("result[1] expected error")
	}
	if results[2].Status() != dombatch.StatusOK {
		t.Error("result[2] expected ok")
	}
}

func TestDelete_ExceedsMax(t *testing.T) {
	col := makeCollection(t, nil)
	docs := &mockDocUpserter{}
	del := &mockDocDeleter{}
	colls := &mockCollReader{col: col}
	embed := &mockEmbedder{}

	svc := New(docs, &mockBatchUpserter{}, del, colls, embed, embed)
	ids := make([]string, MaxBatchSize+1)
	for i := range ids {
		ids[i] = fmt.Sprintf("doc-%d", i)
	}

	results := svc.Delete(context.Background(), "test-col", deleteItemsForTest(ids))
	for _, r := range results {
		if r.Status() != dombatch.StatusError {
			t.Error("expected all errors for oversized batch")
		}
	}
}

func TestDelete_CollectionNotFound(t *testing.T) {
	docs := &mockDocUpserter{}
	del := &mockDocDeleter{}
	colls := &mockCollReader{err: domain.ErrNotFound}
	embed := &mockEmbedder{}

	svc := New(docs, &mockBatchUpserter{}, del, colls, embed, embed)
	results := svc.Delete(context.Background(), "nonexistent", deleteItemsForTest([]string{"a"}))

	if results[0].Status() != dombatch.StatusError {
		t.Error("expected error for nonexistent collection")
	}
	if !errors.Is(results[0].Err(), domain.ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", results[0].Err())
	}
}

// --- Batch embedding specific tests ---

func TestUpsert_BatchEmbedUsed(t *testing.T) {
	col := makeCollection(t, nil)
	docs := &mockDocUpserter{}
	del := &mockDocDeleter{}
	colls := &mockCollReader{col: col}
	embed := &mockEmbedder{result: domain.EmbeddingResult{Embedding: []float32{0.1, 0.2, 0.3}}}

	svc := New(docs, &mockBatchUpserter{}, del, colls, embed, embed)
	items := []domdoc.Document{makeDoc(t, "a"), makeDoc(t, "b")}
	results := svc.Upsert(context.Background(), "test-col", upsertItemsForTest(items))

	for i, r := range results {
		if r.Status() != dombatch.StatusOK {
			t.Errorf("result[%d] expected ok, got %v", i, r.Err())
		}
	}
	// BatchEmbed должен быть вызван один раз, а не Embed поштучно
	if embed.batchCalls != 1 {
		t.Errorf("expected 1 batch call, got %d", embed.batchCalls)
	}
	if embed.callCount != 0 {
		t.Errorf("expected 0 single Embed calls, got %d", embed.callCount)
	}
}

func TestUpsert_NilBatchEmbedFallback(t *testing.T) {
	col := makeCollection(t, nil)
	docs := &mockDocUpserter{}
	del := &mockDocDeleter{}
	colls := &mockCollReader{col: col}
	embed := &mockEmbedder{result: domain.EmbeddingResult{Embedding: []float32{0.1, 0.2, 0.3}}}

	// batchEmbed = nil → fallback на поштучный Embed через BatchFallback
	svc := New(docs, &mockBatchUpserter{}, del, colls, embed, nil)
	items := []domdoc.Document{makeDoc(t, "a"), makeDoc(t, "b")}
	results := svc.Upsert(context.Background(), "test-col", upsertItemsForTest(items))

	for i, r := range results {
		if r.Status() != dombatch.StatusOK {
			t.Errorf("result[%d] expected ok, got %v", i, r.Err())
		}
	}
	// Fallback должен вызвать Embed по одному на каждый текст
	if embed.callCount != 2 {
		t.Errorf("expected 2 single Embed calls (fallback), got %d", embed.callCount)
	}
}

func TestUpsert_AllValidEmbedded(t *testing.T) {
	col := makeCollection(t, []field.Field{makeField(t, "lang", field.Tag)})
	docs := &mockDocUpserter{}
	del := &mockDocDeleter{}
	colls := &mockCollReader{col: col}
	embed := &mockEmbedder{result: domain.EmbeddingResult{Embedding: []float32{0.1, 0.2, 0.3}}}

	svc := New(docs, &mockBatchUpserter{}, del, colls, embed, embed)
	items := []domdoc.Document{
		makeDocWithTags(t, "a", map[string]string{"lang": "go"}),     // valid — known tag
		makeDocWithTags(t, "b", map[string]string{"unknown": "val"}), // valid — stored field
		makeDoc(t, "c"), // valid
	}
	results := svc.Upsert(context.Background(), "test-col", upsertItemsForTest(items))

	for i, r := range results {
		if r.Status() != dombatch.StatusOK {
			t.Errorf("result[%d] expected ok, got %v", i, r.Err())
		}
	}
	// BatchEmbed вызван с 3 текстами (все валидные)
	if embed.batchCalls != 1 {
		t.Errorf("expected 1 batch call, got %d", embed.batchCalls)
	}
}

func TestBatchRejectsMalformedVectorsBeforeWriting(t *testing.T) {
	for _, vectors := range [][][]float32{{{1, 2, 3}}, {{1}, {1, 2, 3}}} {
		repo := &mockBatchUpserter{}
		embed := &mockEmbedder{batchResult: domain.BatchEmbeddingResult{Embeddings: vectors}}
		svc := New(
			&mockDocUpserter{},
			repo,
			&mockDocDeleter{},
			&mockCollReader{col: makeCollection(t, nil)},
			embed,
			embed,
		)
		results := svc.Upsert(context.Background(), "test-col",
			upsertItemsForTest([]domdoc.Document{makeDoc(t, "a"), makeDoc(t, "b")}))
		for _, result := range results {
			if result.Err() == nil {
				t.Fatal("invalid embedding accepted")
			}
		}
	}
}

func TestContextFallbackAndPerParentWriteErrors(t *testing.T) {
	stored := map[string]int{}
	repo := &mockDocUpserter{fn: func(doc *domdoc.Document) (bool, error) {
		if doc.ID() == "writefail" {
			return false, errors.New("write failed")
		}
		stored[doc.ID()] = len(doc.Fragments())
		return true, nil
	}}
	embed := &mockEmbedder{
		batchErr: domain.ErrEmbeddingInputTooLong,
		fn: func(text string) (domain.EmbeddingResult, error) {
			if len(text) > 8 {
				return domain.EmbeddingResult{}, domain.ErrEmbeddingInputTooLong
			}
			if text == "bad" {
				return domain.EmbeddingResult{}, domain.ErrEmbeddingProviderError
			}
			return domain.EmbeddingResult{Embedding: []float32{1, 0, 0}}, nil
		},
	}
	svc := New(
		repo,
		&mockBatchUpserter{},
		&mockDocDeleter{},
		&mockCollReader{col: makeCollection(t, nil)},
		embed,
		embed,
	)
	large, _ := domdoc.New("large", "long text that requires splitting", nil, nil)
	small, _ := domdoc.New("small", "okay", nil, nil)
	bad, _ := domdoc.New("bad", "bad", nil, nil)
	writefail, _ := domdoc.New("writefail", "okay", nil, nil)
	results := svc.Upsert(context.Background(), "test-col",
		upsertItemsForTest([]domdoc.Document{large, small, bad, writefail}))
	if results[0].Err() != nil || results[1].Err() != nil || results[2].Err() == nil || results[3].Err() == nil {
		t.Fatalf("results %+v", results)
	}
	if stored["large"] < 2 || stored["small"] != 1 || len(stored) != 2 {
		t.Fatalf("stored %+v", stored)
	}
}

func TestBatchDeadlineReachesProvider(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Hour)
	defer cancel()
	observed := false
	start := time.Now()
	embed := &mockEmbedder{result: domain.EmbeddingResult{Embedding: []float32{1, 0, 0}}}
	embed.observeContext = func(ctx context.Context) {
		observed = true
		deadline, ok := ctx.Deadline()
		if !ok || deadline.After(start.Add(301*time.Second)) {
			t.Fatalf("missing batch deadline: %v", deadline)
		}
	}
	svc := New(
		&mockDocUpserter{},
		&mockBatchUpserter{},
		&mockDocDeleter{},
		&mockCollReader{col: makeCollection(t, nil)},
		embed,
		embed,
	)
	results := svc.Upsert(ctx, "test-col", upsertItemsForTest([]domdoc.Document{makeDoc(t, "deadline")}))
	if !observed || len(results) != 1 || results[0].Err() != nil {
		t.Fatalf("batch provider not reached: %v", results)
	}
}

func TestUpsertKeepsCompletedEmbeddingPrefixOnLaterFailure(t *testing.T) {
	docs := &mockDocUpserter{}
	embed := &mockEmbedder{batchResult: domain.BatchEmbeddingResult{Embeddings: [][]float32{{1, 0, 0}}, TotalTokens: 7},
		batchErr: domain.ErrEmbeddingProviderError}
	service := New(
		docs,
		&mockBatchUpserter{},
		&mockDocDeleter{},
		&mockCollReader{col: makeCollection(t, nil)},
		embed,
		embed,
	)
	ctx, usage := domain.NewContextWithUsage(context.Background())
	results := service.Upsert(ctx, "test-col",
		upsertItemsForTest([]domdoc.Document{makeDoc(t, "completed"), makeDoc(t, "failed")}))
	if results[0].Err() != nil || !errors.Is(results[1].Err(), domain.ErrEmbeddingProviderError) ||
		docs.callCount != 1 {
		t.Fatalf("successful prefix discarded: %v calls %d", results, docs.callCount)
	}
	if usage.TotalTokens != 7 {
		t.Fatalf("completed usage lost: %+v", usage)
	}
}

func upsertItemsForTest(docs []domdoc.Document) []dombatch.UpsertItem {
	items := make([]dombatch.UpsertItem, len(docs))
	for i, doc := range docs {
		items[i].Document = doc
	}
	return items
}
func deleteItemsForTest(ids []string) []dombatch.DeleteItem {
	items := make([]dombatch.DeleteItem, len(ids))
	for i, id := range ids {
		items[i].ID = id
	}
	return items
}
