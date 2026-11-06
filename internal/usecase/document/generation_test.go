package document

import (
	"context"
	"errors"
	"testing"

	"github.com/kailas-cloud/vecdex/internal/domain"
	domdoc "github.com/kailas-cloud/vecdex/internal/domain/document"
	"github.com/kailas-cloud/vecdex/internal/domain/document/patch"
)

func TestEverySourceOperationRejectsRecreatedCollection(t *testing.T) {
	col := makeCollection(t, nil).WithGeneration("new")
	embedder := &mockEmbedder{observeContext: func(context.Context) { t.Fatal("stale handle called embedder") }}
	svc := New(&mockDocRepo{}, &mockCollReader{col: col}, embedder, embedder)
	ctx := domain.WithExpectedCollectionGeneration(context.Background(), "old")
	doc := makeDoc(t, "source", "text")
	content := "updated"
	p, err := patch.NewSource(&content, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	operations := map[string]func() error{
		"upsert": func() error { _, err := svc.Upsert(ctx, "test-col", &doc); return err },
		"get":    func() error { _, err := svc.Get(ctx, "test-col", "source"); return err },
		"list":   func() error { _, _, err := svc.List(ctx, "test-col", "", 10); return err },
		"patch":  func() error { _, err := svc.Patch(ctx, "test-col", "source", p); return err },
		"delete": func() error { return svc.Delete(ctx, "test-col", "source") },
		"count":  func() error { _, err := svc.Count(ctx, "test-col"); return err },
		"read source": func() error {
			_, err := svc.ReadSource(ctx, "test-col", "source", domdoc.SourceProjection{})
			return err
		},
		"list sources": func() error {
			_, _, err := svc.ListSources(ctx, "test-col", "", 10, domdoc.SourceProjection{})
			return err
		},
	}
	for name, operation := range operations {
		t.Run(name, func(t *testing.T) {
			if err := operation(); !errors.Is(err, domain.ErrCollectionGenerationMismatch) {
				t.Fatalf("stale collection accepted: %v", err)
			}
		})
	}
}

func TestCreateOnlyCannotBecomePatchOrDelete(t *testing.T) {
	svc := New(nil, nil, nil, nil)
	ctx := domain.WithRequireAbsent(context.Background(), true)
	if err := svc.Delete(ctx, "notes", "source"); !errors.Is(err, domain.ErrInvalidSchema) {
		t.Fatal(err)
	}
	if _, err := svc.Patch(ctx, "notes", "source", patch.Patch{}); !errors.Is(err, domain.ErrInvalidSchema) {
		t.Fatal(err)
	}
	version := "version"
	_, err := svc.Upsert(domain.WithExpectedVersion(ctx, &version), "notes", &domdoc.Document{})
	if !errors.Is(err, domain.ErrInvalidSchema) {
		t.Fatal(err)
	}
}
