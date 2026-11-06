package search

import (
	"context"
	"errors"
	"testing"

	"github.com/kailas-cloud/vecdex/internal/domain"
	domcol "github.com/kailas-cloud/vecdex/internal/domain/collection"
	"github.com/kailas-cloud/vecdex/internal/domain/search/mode"
)

type changingCollections struct{ calls int }

func (c *changingCollections) Get(context.Context, string) (domcol.Collection, error) {
	c.calls++
	generation := "old"
	if c.calls > 1 {
		generation = "new"
	}
	return domcol.Reconstruct("notes", domcol.TypeText, nil, 3, 0, 1).WithGeneration(generation), nil
}

func TestSearchRejectsStaleCollectionBeforeEmbedding(t *testing.T) {
	embedder := &mockEmbedder{}
	svc := New(&mockRepo{}, &mockColls{col: domcol.Collection{}.WithGeneration("new")}, embedder)
	ctx := domain.WithExpectedCollectionGeneration(context.Background(), "old")
	_, _, err := svc.Search(ctx, "notes", makeSearchRequest(t, mode.Hybrid))
	if !errors.Is(err, domain.ErrCollectionGenerationMismatch) || embedder.called {
		t.Fatalf("stale search: %v embedded=%t", err, embedder.called)
	}
}

func TestSimilarFencesSecondCollectionResolve(t *testing.T) {
	collections := &changingCollections{}
	svc := New(&mockRepo{}, collections, &mockEmbedder{})
	_, _, err := svc.Similar(context.Background(), "notes", "source", makeSimilarRequest(t))
	if !errors.Is(err, domain.ErrCollectionGenerationMismatch) {
		t.Fatalf("similar crossed collection recreation: %v", err)
	}
}
