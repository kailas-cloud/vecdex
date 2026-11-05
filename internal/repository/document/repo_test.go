package document

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/kailas-cloud/vecdex/internal/db"
	"github.com/kailas-cloud/vecdex/internal/domain"
	domdoc "github.com/kailas-cloud/vecdex/internal/domain/document"
	"github.com/kailas-cloud/vecdex/internal/domain/document/patch"
	"github.com/kailas-cloud/vecdex/internal/domain/storage"
)

// --- Upsert ---

func TestGet_HappyPath(t *testing.T) {
	repo, ms := newTestRepo(t)
	ctx := testContext()

	ms.hgetAllFn = func(_ context.Context, key string) (map[string]string, error) {
		if key != testLayout().Document("doc-1") {
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
	ctx := testContext()

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
	ctx := testContext()
	store.catalogIDsFn = func(_ context.Context, key, after string, limit int) ([]string, error) {
		if key != testLayout().Catalog(0) || after != "" || limit != 8 {
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
	after, err := decodeDocumentCursor(testLayout().Base(), next)
	if err != nil || after != "doc-2" {
		t.Fatalf("cursor: %q %v", next, err)
	}
}

func TestList_Empty(t *testing.T) {
	repo, _ := newTestRepo(t)
	docs, next, err := repo.List(testContext(), "notes", "", 10)
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
	cursor := encodeDocumentCursor(testLayout().Base(), "doc-2")
	docs, next, err := repo.List(testContext(), "notes", cursor, 2)
	if err != nil || len(docs) != 1 || docs[0].ID() != "doc-3" || next != "" {
		t.Fatalf("last page: %v %q %v", docs, next, err)
	}
}

// --- Patch ---

func TestBatchUpsert_Empty(t *testing.T) {
	repo, _ := newTestRepo(t)
	ctx := testContext()

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
	created, err := repo.Upsert(domain.WithExpectedRevision(testContext(), &expected), "notes", &doc)
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
			if _, err := repo.Upsert(testContext(), "notes", &doc); !errors.Is(err, writeErr) {
				t.Fatalf("upsert error %v", err)
			}
			p, _ := patch.New(nil, map[string]*string{"language": nil}, nil)
			revision := 1
			if _, err := repo.Patch(
				domain.WithExpectedRevision(testContext(), &revision),
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
			if err := repo.Delete(testContext(), "notes", "doc-1"); !errors.Is(err, writeErr) {
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
	if _, err := repo.Patch(testContext(), "notes", "doc-1", p, nil, nil); err != nil {
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
	if err := repo.BatchUpsert(testContext(), "notes", []domdoc.Document{testDocument(t)}); err != nil {
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
		if _, _, err := repo.List(testContext(), "notes", cursor, 20); !errors.Is(err, domain.ErrInvalidSchema) {
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
	fields, children, err := prepareFragmentFields(&doc, testLayout(), "version")
	if err != nil || len(children) != 2 {
		t.Fatalf("children=%d err=%v", len(children), err)
	}
	restored, err := parseHashFields(doc.ID(), fields)
	if err != nil {
		t.Fatal(err)
	}
	vectors := make([]string, len(children))
	for i, child := range children {
		vectors[i] = child.Fields["__vector"]
	}
	if fields["__vector"] != "" || strings.Contains(fields["__parts"], "vector") {
		t.Fatal("parent duplicates fragment vectors")
	}
	if err := hydrateFragments(&restored, fields, vectors); err != nil {
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
	oldLayout, _ := storage.NewCollection("notes", "old-generation")
	cursor := encodeDocumentCursor(oldLayout.Base(), "doc-2")
	ctx := domain.WithCollectionGeneration(testContext(), "new-generation")
	if _, _, err := repo.List(ctx, "notes", cursor, 20); !errors.Is(err, domain.ErrInvalidSchema) {
		t.Fatalf("stale cursor accepted: %v", err)
	}
	if _, _, err := repo.List(testContext(), "other", cursor, 20); !errors.Is(err, domain.ErrInvalidSchema) {
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
	docs, cursor, err := repo.List(testContext(), "notes", "", 2)
	if err != nil || len(docs) != 1 || docs[0].ID() != "b" {
		t.Fatalf("delete during list: %v %v", docs, err)
	}
	if after, err := decodeDocumentCursor(testLayout().Base(), cursor); err != nil || after != "b" {
		t.Fatalf("non-progressing cursor %q %v", cursor, err)
	}
}

func testLayout() storage.Collection {
	layout, err := storage.NewCollection("notes", "fixture")
	if err != nil {
		panic(err)
	}
	return layout
}

func TestGetRetriesWhenReplacementReclaimsOldFragments(t *testing.T) {
	repo, store := newTestRepo(t)
	old := testDocument(t)
	current := domdoc.Reconstruct(old.ID(), "new source", old.Tags(), old.Numerics(), old.Vector(), 2)
	oldFields, _, err := prepareFragmentFields(&old, testLayout(), "old")
	if err != nil {
		t.Fatal(err)
	}
	fields, children, err := prepareFragmentFields(&current, testLayout(), "current")
	if err != nil {
		t.Fatal(err)
	}
	reads := 0
	store.hgetAllFn = func(context.Context, string) (map[string]string, error) {
		reads++
		if reads == 1 {
			return oldFields, nil
		}
		return fields, nil
	}
	vectorReads := 0
	store.hgetFieldMultiFn = func(_ context.Context, keys []string, field string) ([]string, error) {
		vectorReads++
		if vectorReads == 1 {
			return []string{""}, nil
		}
		if field != "__vector" || len(keys) != 1 || keys[0] != children[0].Key {
			t.Fatal("wrong fragment projection")
		}
		return []string{children[0].Fields["__vector"]}, nil
	}
	got, err := repo.Get(testContext(), "notes", old.ID())
	if err != nil || got.Content() != current.Content() || len(got.Fragments()) != 1 {
		t.Fatalf("replacement read: content=%q err=%v", got.Content(), err)
	}
	if vectorReads != 2 {
		t.Fatalf("fragment reads=%d", vectorReads)
	}
}

func TestGetRejectsMissingCommittedFragment(t *testing.T) {
	repo, store := newTestRepo(t)
	doc := testDocument(t)
	fields, _, err := prepareFragmentFields(&doc, testLayout(), "current")
	if err != nil {
		t.Fatal(err)
	}
	store.hgetAllFn = func(context.Context, string) (map[string]string, error) { return fields, nil }
	store.hgetFieldMultiFn = func(context.Context, []string, string) ([]string, error) { return []string{""}, nil }
	if _, err := repo.Get(testContext(), "notes", doc.ID()); err == nil {
		t.Fatal("accepted missing committed payload")
	}
}

func TestHydratedSourceRetainsRepresentativeVector(t *testing.T) {
	doc := domdoc.Reconstruct("mixed", "中文other", nil, nil, []float32{1, 0, 0}, 1)
	parts := []domdoc.Fragment{
		{Content: "中文", Vector: []float32{1, 0, 0}},
		{Content: "other", Vector: []float32{0, 1, 0}},
	}
	doc.SetFragments(parts)
	fields, children, err := prepareFragmentFields(&doc, testLayout(), "mixed")
	if err != nil {
		t.Fatal(err)
	}
	restored, err := parseHashFields(doc.ID(), fields)
	if err != nil {
		t.Fatal(err)
	}
	vectors := []string{children[0].Fields["__vector"], children[1].Fields["__vector"]}
	if err := hydrateFragments(&restored, fields, vectors); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(restored.Vector(), domdoc.RepresentativeVector(parts)) {
		t.Fatal("source representative changed")
	}
}

func TestUpsertCarriesOpaqueVersionAndReturnsCommittedToken(t *testing.T) {
	repo, store := newTestRepo(t)
	doc := testDocument(t)
	expected := "previous-opaque-version"
	store.mutateFn = func(_ context.Context, m *db.HashMutation) (db.HashMutationResult, error) {
		if m.ExpectedVersion == nil || *m.ExpectedVersion != expected {
			t.Fatal("opaque condition lost")
		}
		return db.HashMutationResult{Revision: 2, Version: "next-opaque-version"}, nil
	}
	if _, err := repo.Upsert(domain.WithExpectedVersion(testContext(), &expected), "notes", &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Version() != "next-opaque-version" {
		t.Fatal("committed version was not returned")
	}
}

func TestDeleteCarriesOpaqueVersion(t *testing.T) {
	repo, store := newTestRepo(t)
	expected := "previous-opaque-version"
	store.mutateFn = func(_ context.Context, m *db.HashMutation) (db.HashMutationResult, error) {
		if !m.Delete || m.ExpectedVersion == nil || *m.ExpectedVersion != expected {
			t.Fatal("delete condition lost")
		}
		return db.HashMutationResult{}, nil
	}
	if err := repo.Delete(domain.WithExpectedVersion(testContext(), &expected), "notes", "source"); err != nil {
		t.Fatal(err)
	}
}

func TestMetadataPatchUsesOpaqueInternalCAS(t *testing.T) {
	repo, store := newTestRepo(t)
	vector := testVector(1024)
	store.hgetAllFn = func(context.Context, string) (map[string]string, error) {
		return map[string]string{
			"__content":        "hello world",
			"__vector":         vectorToBytes(vector),
			"__revision":       "7",
			"__source_version": "incarnation-version",
		}, nil
	}
	store.mutateFn = func(_ context.Context, m *db.HashMutation) (db.HashMutationResult, error) {
		if m.ExpectedVersion == nil || *m.ExpectedVersion != "incarnation-version" || m.ExpectedRevision != nil {
			t.Fatal("metadata patch did not protect the source incarnation")
		}
		return db.HashMutationResult{Revision: 8, Version: "next"}, nil
	}
	value := "updated"
	p, err := patch.New(nil, map[string]*string{"language": &value}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Patch(testContext(), "notes", "source", p, nil, nil); err != nil {
		t.Fatal(err)
	}
}
