package vecdex

import (
	"context"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	dombatch "github.com/kailas-cloud/vecdex/internal/domain/batch"
)

func TestTextIngestorPreservesSourceDocuments(t *testing.T) {
	stored := map[string]string{}
	var mu sync.Mutex
	batch := &mockBatchUC{upsertFn: func(_ context.Context, _ string, docs []dombatch.UpsertItem) []dombatch.Result {
		mu.Lock()
		defer mu.Unlock()
		results := make([]dombatch.Result, len(docs))
		for i, doc := range docs {
			stored[doc.ID()] = doc.Content()
			if len(doc.Tags()) != 0 || len(doc.Numerics()) != 0 {
				t.Errorf("ingestor injected fragment metadata into %s", doc.ID())
			}
			results[i] = dombatch.NewOK(doc.ID())
		}
		return results
	}}
	ingestor := NewTextIngestor(newDocSvc(nil, batch), &IngestorConfig{BatchSize: 1, Parallelism: 2})
	long := strings.Repeat("original source text. ", 1000)
	response, err := ingestor.Upsert(context.Background(), []BatchUpsertItem{
		{ID: "a", Content: long}, {ID: "b", Content: "other"},
	})
	if err != nil || response.Succeeded != 2 || stored["a"] != long {
		t.Fatalf("source ingest: response=%+v err=%v", response, err)
	}
	if _, err := ingestor.Upsert(context.Background(), []BatchUpsertItem{{ID: "a", Content: "short"}}); err != nil {
		t.Fatal(err)
	}
	if want := map[string]string{"a": "short", "b": "other"}; !reflect.DeepEqual(stored, want) {
		t.Fatalf("replacement left unexpected documents: %v", stored)
	}
}

func TestTextIngestorBatchesInParallel(t *testing.T) {
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	batch := &mockBatchUC{upsertFn: func(_ context.Context, _ string, docs []dombatch.UpsertItem) []dombatch.Result {
		entered <- struct{}{}
		<-release
		return []dombatch.Result{dombatch.NewOK(docs[0].ID())}
	}}
	ingestor := NewTextIngestor(newDocSvc(nil, batch), &IngestorConfig{BatchSize: 1, Parallelism: 2})
	done := make(chan error, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go func() {
		_, err := ingestor.Upsert(ctx, []BatchUpsertItem{{ID: "a", Content: "a"}, {ID: "b", Content: "b"}})
		done <- err
	}()
	// Both batches must start before either is released.
	for range 2 {
		select {
		case <-entered:
		case <-ctx.Done():
			close(release)
			t.Fatal("batches did not start concurrently")
		}
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
