package document

import (
	"context"
	"errors"
	"testing"

	"github.com/kailas-cloud/vecdex/internal/db"
	"github.com/kailas-cloud/vecdex/internal/domain"
	domdoc "github.com/kailas-cloud/vecdex/internal/domain/document"
	"github.com/kailas-cloud/vecdex/internal/domain/document/patch"
)

// --- Upsert ---

func TestGet_HappyPath(t *testing.T) {
	repo, ms := newTestRepo(t)
	ctx := context.Background()

	ms.hgetAllFn = func(_ context.Context, key string) (map[string]string, error) {
		if key != "vecdex:v2:{notes}:doc:doc-1" {
			t.Errorf("unexpected key: %s", key)
		}
		return map[string]string{
			"__content":    "hello world",
			"__vector":     vectorToBytes([]float32{0.1, 0.2}),
			"language":     "go",
			"__n:priority": "1.5",
		}, nil
	}

	doc, err := repo.Get(ctx, "notes", "doc-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if doc.ID() != "doc-1" {
		t.Fatalf("expected ID doc-1, got %s", doc.ID())
	}
	if doc.Content() != "hello world" {
		t.Fatalf("expected content 'hello world', got %s", doc.Content())
	}
	if doc.Tags()["language"] != "go" {
		t.Fatalf("expected tag language=go, got %v", doc.Tags())
	}
	if doc.Numerics()["priority"] != 1.5 {
		t.Fatalf("expected numeric priority=1.5, got %v", doc.Numerics())
	}
}

func TestGet_NotFound(t *testing.T) {
	repo, ms := newTestRepo(t)
	ctx := context.Background()

	ms.hgetAllFn = func(_ context.Context, _ string) (map[string]string, error) {
		return map[string]string{}, nil // empty map = not found
	}

	_, err := repo.Get(ctx, "notes", "nonexistent")
	if !errors.Is(err, domain.ErrDocumentNotFound) {
		t.Fatalf("expected ErrDocumentNotFound, got %v", err)
	}
}

// --- Delete ---

func TestList_HappyPath(t *testing.T) {
	repo, store := newTestRepo(t)
	ctx := context.Background()
	store.catalogIDsFn = func(_ context.Context, key, after string, limit int) ([]string, error) {
		if key != domain.DocumentCatalogKey("notes", "") || after != "" || limit != 3 {
			t.Fatalf("catalog request %s %s %d", key, after, limit)
		}
		return []string{"doc-1", "doc-2", "doc-3"}, nil
	}
	store.hgetAllMultiFn = func(_ context.Context, keys []string) ([]map[string]string, error) {
		if len(keys) != 2 {
			t.Fatalf("fetched %d parents instead of two", len(keys))
		}
		return []map[string]string{
			{"__content": "hello", "language": "go"},
			{"__content": "world", "language": "py"},
		}, nil
	}
	docs, next, err := repo.List(ctx, "notes", "", 2)
	if err != nil || len(docs) != 2 {
		t.Fatalf("page: %v %v", docs, err)
	}
	if docs[0].ID() != "doc-1" || docs[1].ID() != "doc-2" {
		t.Fatal("page order changed")
	}
	after, err := decodeDocumentCursor(domain.CollectionBase("notes"), next)
	if err != nil || after != "doc-2" {
		t.Fatalf("cursor: %q %v", next, err)
	}
}

func TestList_Empty(t *testing.T) {
	repo, _ := newTestRepo(t)
	docs, next, err := repo.List(context.Background(), "notes", "", 10)
	if err != nil || len(docs) != 0 || next != "" {
		t.Fatalf("empty page: %v %q %v", docs, next, err)
	}
}

func TestList_WithCursor(t *testing.T) {
	repo, store := newTestRepo(t)
	store.catalogIDsFn = func(_ context.Context, _ string, after string, _ int) ([]string, error) {
		if after != "doc-2" {
			t.Fatalf("after %q", after)
		}
		return []string{"doc-3"}, nil
	}
	store.hgetAllFn = func(context.Context, string) (map[string]string, error) {
		return map[string]string{"__content": "last"}, nil
	}
	cursor := encodeDocumentCursor(domain.CollectionBase("notes"), "doc-2")
	docs, next, err := repo.List(context.Background(), "notes", cursor, 2)
	if err != nil || len(docs) != 1 || docs[0].ID() != "doc-3" || next != "" {
		t.Fatalf("last page: %v %q %v", docs, next, err)
	}
}

// --- Patch ---

func TestBatchUpsert_Empty(t *testing.T) {
	repo, _ := newTestRepo(t)
	ctx := context.Background()

	err := repo.BatchUpsert(ctx, "notes", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// --- vectorToBytes / bytesToVector roundtrip ---

func TestVectorRoundtrip(t *testing.T) {
	original := []float32{0.1, 0.2, 0.3, 1.0, -0.5}
	encoded := vectorToBytes(original)
	decoded := bytesToVector(encoded)
	if len(decoded) != len(original) {
		t.Fatalf("expected %d elements, got %d", len(original), len(decoded))
	}
	for i := range original {
		if original[i] != decoded[i] {
			t.Errorf("mismatch at %d: %f != %f", i, original[i], decoded[i])
		}
	}
}

func TestAtomicUpsert(t *testing.T) {
	repo, store := newTestRepo(t)
	doc := testDocument(t)
	expected := 7
	store.mutateFn = func(_ context.Context, m *db.HashMutation) (db.HashMutationResult, error) {
		if !m.Replace || m.ExpectedRevision == nil || *m.ExpectedRevision != 7 || m.Set["__content"] != "hello world" {
			t.Fatalf("mutation %+v", m)
		}
		return db.HashMutationResult{Revision: 8, Created: false}, nil
	}
	created, err := repo.Upsert(domain.WithExpectedRevision(context.Background(), &expected), "notes", &doc)
	if err != nil || created || doc.Revision() != 8 {
		t.Fatalf("created=%t revision=%d err=%v", created, doc.Revision(), err)
	}
}
func TestAtomicWriteErrors(t *testing.T) {
	for _, writeErr := range []error{domain.NewRevisionConflict(4), domain.ErrDocumentNotFound, errors.New("OOM")} {
		t.Run(writeErr.Error(), func(t *testing.T) {
			repo, store := newTestRepo(t)
			doc := testDocument(t)
			store.hgetAllFn = func(context.Context, string) (map[string]string, error) { return buildHashFields(&doc), nil }
			store.mutateFn = func(context.Context, *db.HashMutation) (db.HashMutationResult, error) {
				return db.HashMutationResult{}, writeErr
			}
			if _, err := repo.Upsert(context.Background(), "notes", &doc); !errors.Is(err, writeErr) {
				t.Fatalf("upsert error %v", err)
			}
			p, _ := patch.New(nil, map[string]*string{"language": nil}, nil)
			revision := 1
			if err := repo.Patch(
				domain.WithExpectedRevision(context.Background(), &revision),
				"notes",
				"doc-1",
				p,
				nil,
				nil,
			); !errors.Is(
				err,
				writeErr,
			) {
				t.Fatalf("patch error %v", err)
			}
			if err := repo.Delete(context.Background(), "notes", "doc-1"); !errors.Is(err, writeErr) {
				t.Fatalf("delete error %v", err)
			}
		})
	}
}
func TestAtomicPatchRemovesFields(t *testing.T) {
	repo, store := newTestRepo(t)
	doc := testDocument(t)
	store.hgetAllFn = func(context.Context, string) (map[string]string, error) {
		fields := buildHashFields(&doc)
		fields["__revision"] = "1"
		return fields, nil
	}
	store.mutateFn = func(_ context.Context, m *db.HashMutation) (db.HashMutationResult, error) {
		if !m.Replace || m.Set["language"] != "" || m.ExpectedRevision == nil || len(m.Children) != 1 {
			t.Fatalf("mutation %+v", m)
		}
		return db.HashMutationResult{Revision: 2}, nil
	}
	p, _ := patch.New(nil, map[string]*string{"language": nil}, nil)
	if err := repo.Patch(context.Background(), "notes", "doc-1", p, nil, nil); err != nil {
		t.Fatal(err)
	}
}

func TestBatchUsesAtomicReplacement(t *testing.T) {
	repo, store := newTestRepo(t)
	calls := 0
	store.mutateFn = func(_ context.Context, m *db.HashMutation) (db.HashMutationResult, error) {
		calls++
		if !m.Replace {
			t.Fatal("batch used additive write")
		}
		return db.HashMutationResult{Revision: 2}, nil
	}
	if err := repo.BatchUpsert(context.Background(), "notes", []domdoc.Document{testDocument(t)}); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("calls=%d", calls)
	}
}
func TestInvalidCursorRejectedBeforeStorage(t *testing.T) {
	repo, store := newTestRepo(t)
	store.catalogIDsFn = func(context.Context, string, string, int) ([]string, error) {
		t.Fatal("invalid cursor reached backend")
		return nil, nil
	}
	for _, cursor := range []string{"-1", "9223372036854775807", "bad"} {
		if _, _, err := repo.List(context.Background(), "notes", cursor, 20); !errors.Is(err, domain.ErrInvalidSchema) {
			t.Fatalf("cursor %s: %v", cursor, err)
		}
	}
}

func TestFragmentManifestPreservesExactSourceAndVectors(t *testing.T) {
	doc := testDocument(t)
	source := "Русский 中文\n\nsecond part"
	doc = domdoc.Reconstruct(doc.ID(), source, doc.Tags(), doc.Numerics(), doc.Vector(), 1)
	doc.SetFragments(
		[]domdoc.Fragment{
			{Content: "Русский 中文\n\n", Vector: doc.Vector()},
			{Content: "second part", Vector: doc.Vector()},
		},
	)
	fields, children, err := prepareFragmentFields(&doc, "base", "version")
	if err != nil || len(children) != 2 {
		t.Fatalf("children=%d err=%v", len(children), err)
	}
	restored := parseHashFields(doc.ID(), fields)
	if err := hydrateFragments(&restored, fields); err != nil {
		t.Fatal(err)
	}
	if len(restored.Fragments()) != 2 || restored.Fragments()[0].Content+restored.Fragments()[1].Content != source {
		t.Fatal("source changed")
	}
	for _, part := range restored.Fragments() {
		if len(part.Vector) != len(doc.Vector()) || part.Vector[12] != doc.Vector()[12] {
			t.Fatal("vector bits changed")
		}
	}
}

func TestCursorBoundToCollectionGeneration(t *testing.T) {
	repo, store := newTestRepo(t)
	store.catalogIDsFn = func(context.Context, string, string, int) ([]string, error) {
		t.Fatal("stale cursor reached storage")
		return nil, nil
	}
	cursor := encodeDocumentCursor(domain.CollectionDataBase("notes", "old-generation"), "doc-2")
	ctx := domain.WithCollectionGeneration(context.Background(), "new-generation")
	if _, _, err := repo.List(ctx, "notes", cursor, 20); !errors.Is(err, domain.ErrInvalidSchema) {
		t.Fatalf("stale cursor accepted: %v", err)
	}
	if _, _, err := repo.List(context.Background(), "other", cursor, 20); !errors.Is(err, domain.ErrInvalidSchema) {
		t.Fatalf("foreign cursor accepted: %v", err)
	}
}

func TestListAdvancesPastConcurrentlyDeletedParent(t *testing.T) {
	repo, store := newTestRepo(t)
	store.catalogIDsFn = func(context.Context, string, string, int) ([]string, error) {
		return []string{"a", "b", "c"}, nil
	}
	store.hgetAllMultiFn = func(context.Context, []string) ([]map[string]string, error) {
		return []map[string]string{{}, {"__content": "survivor"}}, nil
	}
	docs, cursor, err := repo.List(context.Background(), "notes", "", 2)
	if err != nil || len(docs) != 1 || docs[0].ID() != "b" {
		t.Fatalf("delete during list: %v %v", docs, err)
	}
	if after, err := decodeDocumentCursor(domain.CollectionBase("notes"), cursor); err != nil || after != "b" {
		t.Fatalf("non-progressing cursor %q %v", cursor, err)
	}
}
