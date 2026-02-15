package document

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/kailas-cloud/vecdex/internal/db"
	"github.com/kailas-cloud/vecdex/internal/domain"
	"github.com/kailas-cloud/vecdex/internal/domain/document/patch"
)

// --- Upsert ---

func TestUpsert_Create(t *testing.T) {
	repo, ms := newTestRepo(t)
	ctx := context.Background()
	doc := testDocument(t)

	ms.existsFn = func(_ context.Context, key string) (bool, error) {
		if key != "vecdex:notes:doc-1" {
			t.Errorf("unexpected key: %s", key)
		}
		return false, nil
	}
	ms.jsonSetFn = func(_ context.Context, key, path string, _ []byte) error {
		if key != "vecdex:notes:doc-1" {
			t.Errorf("unexpected key: %s", key)
		}
		if path != "$" {
			t.Errorf("unexpected path: %s", path)
		}
		return nil
	}

	created, err := repo.Upsert(ctx, "notes", &doc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !created {
		t.Fatal("expected created=true for new doc")
	}
}

func TestUpsert_Update(t *testing.T) {
	repo, ms := newTestRepo(t)
	ctx := context.Background()
	doc := testDocument(t)

	ms.existsFn = func(_ context.Context, _ string) (bool, error) { return true, nil }
	ms.jsonSetFn = func(_ context.Context, _ string, _ string, _ []byte) error { return nil }

	created, err := repo.Upsert(ctx, "notes", &doc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if created {
		t.Fatal("expected created=false for existing doc")
	}
}

func TestUpsert_JSONSetError(t *testing.T) {
	repo, ms := newTestRepo(t)
	ctx := context.Background()
	doc := testDocument(t)

	ms.existsFn = func(_ context.Context, _ string) (bool, error) { return false, nil }
	ms.jsonSetFn = func(_ context.Context, _ string, _ string, _ []byte) error {
		return errors.New("OOM")
	}

	_, err := repo.Upsert(ctx, "notes", &doc)
	if err == nil {
		t.Fatal("expected error on JSON.SET failure")
	}
}

// --- Get ---

func TestGet_HappyPath(t *testing.T) {
	repo, ms := newTestRepo(t)
	ctx := context.Background()

	jsonResult := `[{"__content":"hello world","__vector":[0.1,0.2],"language":"go","priority":1.5}]`
	ms.jsonGetFn = func(_ context.Context, key string, _ ...string) ([]byte, error) {
		if key != "vecdex:notes:doc-1" {
			t.Errorf("unexpected key: %s", key)
		}
		return []byte(jsonResult), nil
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

	ms.jsonGetFn = func(_ context.Context, _ string, _ ...string) ([]byte, error) {
		return nil, db.ErrKeyNotFound
	}

	_, err := repo.Get(ctx, "notes", "nonexistent")
	if !errors.Is(err, domain.ErrDocumentNotFound) {
		t.Fatalf("expected ErrDocumentNotFound, got %v", err)
	}
}

// --- Delete ---

func TestDelete_HappyPath(t *testing.T) {
	repo, ms := newTestRepo(t)
	ctx := context.Background()

	ms.existsFn = func(_ context.Context, key string) (bool, error) {
		return key == "vecdex:notes:doc-1", nil
	}
	ms.delFn = func(_ context.Context, _ string) error { return nil }

	err := repo.Delete(ctx, "notes", "doc-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestDelete_NotFound(t *testing.T) {
	repo, ms := newTestRepo(t)
	ctx := context.Background()

	ms.existsFn = func(_ context.Context, _ string) (bool, error) { return false, nil }

	err := repo.Delete(ctx, "notes", "doc-1")
	if !errors.Is(err, domain.ErrDocumentNotFound) {
		t.Fatalf("expected ErrDocumentNotFound, got %v", err)
	}
}

// --- List ---

func TestList_HappyPath(t *testing.T) {
	repo, ms := newTestRepo(t)
	ctx := context.Background()

	ms.searchListFn = func(_ context.Context, _ string, _ string, _ int, _ int, _ []string) (*db.SearchResult, error) {
		return &db.SearchResult{
			Total: 10,
			Entries: []db.SearchEntry{
				{Key: "vecdex:notes:doc-1", Fields: map[string]string{"$": `{"__content":"hello","language":"go"}`}},
				{Key: "vecdex:notes:doc-2", Fields: map[string]string{"$": `{"__content":"world","language":"py"}`}},
				{Key: "vecdex:notes:doc-3", Fields: map[string]string{"$": `{"__content":"extra"}`}},
			},
		}, nil
	}

	docs, nextCursor, err := repo.List(ctx, "notes", "", 2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(docs) != 2 {
		t.Fatalf("expected 2 docs, got %d", len(docs))
	}
	if docs[0].ID() != "doc-1" {
		t.Fatalf("expected first doc ID doc-1, got %s", docs[0].ID())
	}
	if docs[1].ID() != "doc-2" {
		t.Fatalf("expected second doc ID doc-2, got %s", docs[1].ID())
	}
	if nextCursor != "2" {
		t.Fatalf("expected nextCursor=2, got %q", nextCursor)
	}
}

func TestList_Empty(t *testing.T) {
	repo, ms := newTestRepo(t)
	ctx := context.Background()

	ms.searchListFn = func(_ context.Context, _ string, _ string, _ int, _ int, _ []string) (*db.SearchResult, error) {
		return &db.SearchResult{Total: 0}, nil
	}

	docs, nextCursor, err := repo.List(ctx, "notes", "", 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(docs) != 0 {
		t.Fatalf("expected 0 docs, got %d", len(docs))
	}
	if nextCursor != "" {
		t.Fatalf("expected empty cursor, got %q", nextCursor)
	}
}

func TestList_WithCursor(t *testing.T) {
	repo, ms := newTestRepo(t)
	ctx := context.Background()

	ms.searchListFn = func(
		_ context.Context, _ string, _ string, offset int, _ int, _ []string,
	) (*db.SearchResult, error) {
		if offset != 2 {
			t.Errorf("expected offset=2, got %d", offset)
		}
		return &db.SearchResult{
			Total: 3,
			Entries: []db.SearchEntry{
				{Key: "vecdex:notes:doc-3", Fields: map[string]string{"$": `{"__content":"last"}`}},
			},
		}, nil
	}

	docs, nextCursor, err := repo.List(ctx, "notes", "2", 2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(docs) != 1 {
		t.Fatalf("expected 1 doc, got %d", len(docs))
	}
	if nextCursor != "" {
		t.Fatalf("expected empty cursor (no more), got %q", nextCursor)
	}
}

// --- Patch ---

func TestPatch_HappyPath(t *testing.T) {
	repo, ms := newTestRepo(t)
	ctx := context.Background()

	newContent := "updated content"
	p, err := patch.New(&newContent, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error creating patch: %v", err)
	}

	ms.jsonGetFn = func(_ context.Context, _ string, _ ...string) ([]byte, error) {
		return []byte(`[{"__content":"old content","language":"go"}]`), nil
	}
	ms.jsonSetFn = func(_ context.Context, _ string, _ string, data []byte) error {
		var m map[string]any
		if err := json.Unmarshal(data, &m); err != nil {
			t.Fatalf("invalid JSON: %v", err)
		}
		if m["__content"] != "updated content" {
			t.Errorf("expected updated content, got %v", m["__content"])
		}
		return nil
	}

	err = repo.Patch(ctx, "notes", "doc-1", p, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestPatch_NotFound(t *testing.T) {
	repo, ms := newTestRepo(t)
	ctx := context.Background()

	newContent := "updated"
	p, _ := patch.New(&newContent, nil, nil)

	ms.jsonGetFn = func(_ context.Context, _ string, _ ...string) ([]byte, error) {
		return nil, db.ErrKeyNotFound
	}

	err := repo.Patch(ctx, "notes", "doc-1", p, nil)
	if !errors.Is(err, domain.ErrDocumentNotFound) {
		t.Fatalf("expected ErrDocumentNotFound, got %v", err)
	}
}

func TestPatch_DeleteTag(t *testing.T) {
	repo, ms := newTestRepo(t)
	ctx := context.Background()

	p, _ := patch.New(nil, map[string]*string{"language": nil}, nil)

	ms.jsonGetFn = func(_ context.Context, _ string, _ ...string) ([]byte, error) {
		return []byte(`[{"__content":"text","language":"go","priority":1.5}]`), nil
	}
	ms.jsonSetFn = func(_ context.Context, _ string, _ string, data []byte) error {
		if strings.Contains(string(data), `"language"`) {
			t.Error("language field should have been deleted")
		}
		return nil
	}

	err := repo.Patch(ctx, "notes", "doc-1", p, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
