package batch

import (
	"context"
	"errors"
	"testing"

	"github.com/kailas-cloud/vecdex/internal/domain"
	dombatch "github.com/kailas-cloud/vecdex/internal/domain/batch"
	domdoc "github.com/kailas-cloud/vecdex/internal/domain/document"
)

type conditionRecorder struct{ seen map[string]string }

func (r *conditionRecorder) Upsert(ctx context.Context, _ string, doc *domdoc.Document) (bool, error) {
	if err := r.record(ctx, doc.ID()); err != nil {
		return false, err
	}
	doc.SetVersion("written-" + doc.ID())
	return false, nil
}

func (r *conditionRecorder) Delete(ctx context.Context, _, id string) error { return r.record(ctx, id) }

func (r *conditionRecorder) record(ctx context.Context, id string) error {
	r.seen[id] = "unconditional"
	if value := domain.ExpectedVersion(ctx); value != nil {
		r.seen[id] = *value
	}
	if id == "conflict" {
		return domain.NewVersionConflict("current")
	}
	return nil
}

func TestUpsertKeepsConditionsPerItemAndRejectsEmptyBeforeEmbedding(t *testing.T) {
	recorder := &conditionRecorder{seen: map[string]string{}}
	embed := &mockEmbedder{batchResult: domain.BatchEmbeddingResult{
		Embeddings: [][]float32{{1, 0, 0}, {1, 0, 0}, {1, 0, 0}},
	}}
	service := New(recorder, nil, recorder, &mockCollReader{col: makeCollection(t, nil)}, embed, embed)
	version, stale, empty := "expected", "stale", ""
	items := []dombatch.UpsertItem{
		{Document: makeDoc(t, "first"), IfVersion: &version},
		{Document: makeDoc(t, "conflict"), IfVersion: &stale},
		{Document: makeDoc(t, "unconditional")},
		{Document: makeDoc(t, "invalid"), IfVersion: &empty},
	}
	ambient := "must-not-leak"
	ctx := domain.WithExpectedVersion(context.Background(), &ambient)
	results := service.Upsert(ctx, "test-col", items)
	if results[0].Version() != "written-first" || results[2].Version() != "written-unconditional" ||
		!errors.Is(results[1].Err(), domain.ErrRevisionConflict) || !errors.Is(results[3].Err(), domain.ErrInvalidSchema) {
		t.Fatalf("per-item conditions or committed versions lost: %+v", results)
	}
	if recorder.seen["first"] != version || recorder.seen["conflict"] != stale ||
		recorder.seen["unconditional"] != "unconditional" || len(recorder.seen) != 3 {
		t.Fatalf("one source condition leaked into another write: %v", recorder.seen)
	}
	if embed.batchCalls != 1 || len(items[3].Vector()) != 0 {
		t.Fatal("invalid condition entered document preparation")
	}
}

func TestDeleteKeepsConditionsPerItemAndContinuesAfterConflict(t *testing.T) {
	recorder := &conditionRecorder{seen: map[string]string{}}
	service := New(recorder, nil, recorder, &mockCollReader{col: makeCollection(t, nil)}, nil, nil)
	stale, empty := "stale", ""
	results := service.Delete(context.Background(), "test-col", []dombatch.DeleteItem{
		{ID: "conflict", IfVersion: &stale}, {ID: "unconditional"}, {ID: "invalid", IfVersion: &empty},
	})
	if !errors.Is(results[0].Err(), domain.ErrRevisionConflict) || results[1].Status() != dombatch.StatusOK ||
		!errors.Is(results[2].Err(), domain.ErrInvalidSchema) {
		t.Fatalf("per-item conditional delete failed: %+v", results)
	}
	if recorder.seen["conflict"] != stale || recorder.seen["unconditional"] != "unconditional" || len(recorder.seen) != 2 {
		t.Fatalf("one source condition leaked into another delete: %v", recorder.seen)
	}
}
