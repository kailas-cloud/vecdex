package collection

import (
	"context"
	"errors"
	"testing"

	"github.com/kailas-cloud/vecdex/internal/db"
	"github.com/kailas-cloud/vecdex/internal/domain"
)

// --- Create ---

func TestCreate_HappyPath(t *testing.T) {
	repo, ms := newTestRepo(t)
	ctx := context.Background()
	col := testCollection(t)

	ms.existsFn = func(_ context.Context, _ string) (bool, error) { return false, nil }
	ms.hsetFn = func(_ context.Context, key string, _ map[string]string) error {
		if key != "vecdex:collection:test-collection" {
			t.Errorf("unexpected key: %s", key)
		}
		return nil
	}
	ms.createIndexFn = func(_ context.Context, def *db.IndexDefinition) error {
		if def.Name != "vecdex:test-collection:idx" {
			t.Errorf("unexpected index name: %s", def.Name)
		}
		return nil
	}

	err := repo.Create(ctx, col)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCreate_AlreadyExists(t *testing.T) {
	repo, ms := newTestRepo(t)
	ctx := context.Background()
	col := testCollection(t)

	ms.existsFn = func(_ context.Context, _ string) (bool, error) { return true, nil }

	err := repo.Create(ctx, col)
	if !errors.Is(err, domain.ErrAlreadyExists) {
		t.Fatalf("expected ErrAlreadyExists, got %v", err)
	}
}

func TestCreate_HSetError(t *testing.T) {
	repo, ms := newTestRepo(t)
	ctx := context.Background()
	col := testCollection(t)

	ms.existsFn = func(_ context.Context, _ string) (bool, error) { return false, nil }
	ms.hsetFn = func(_ context.Context, _ string, _ map[string]string) error {
		return errors.New("connection lost")
	}

	err := repo.Create(ctx, col)
	if err == nil {
		t.Fatal("expected error on HSET failure")
	}
}

func TestCreate_FTCreateError_RollbackOK(t *testing.T) {
	repo, ms := newTestRepo(t)
	ctx := context.Background()
	col := testCollection(t)

	var delCalled bool
	ms.existsFn = func(_ context.Context, _ string) (bool, error) { return false, nil }
	ms.hsetFn = func(_ context.Context, _ string, _ map[string]string) error { return nil }
	ms.createIndexFn = func(_ context.Context, _ *db.IndexDefinition) error {
		return errors.New("index limit reached")
	}
	ms.delFn = func(_ context.Context, key string) error {
		delCalled = true
		if key != "vecdex:collection:test-collection" {
			t.Errorf("unexpected DEL key: %s", key)
		}
		return nil
	}

	err := repo.Create(ctx, col)
	if err == nil {
		t.Fatal("expected error on FT.CREATE failure")
	}
	if !delCalled {
		t.Error("expected DEL to be called for rollback")
	}
}

// --- Get ---

func TestGet_HappyPath(t *testing.T) {
	repo, ms := newTestRepo(t)
	ctx := context.Background()

	ms.hgetAllFn = func(_ context.Context, key string) (map[string]string, error) {
		if key != "vecdex:collection:test-collection" {
			t.Errorf("unexpected key: %s", key)
		}
		return map[string]string{
			"name":        "test-collection",
			"type":        "json",
			"fields_json": `[{"name":"language","type":"tag"}]`,
			"vector_dim":  "1024",
			"created_at":  "1700000000000",
		}, nil
	}

	col, err := repo.Get(ctx, "test-collection")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if col.Name() != "test-collection" {
		t.Fatalf("expected name test-collection, got %s", col.Name())
	}
	if col.VectorDim() != 1024 {
		t.Fatalf("expected vector_dim 1024, got %d", col.VectorDim())
	}
	if len(col.Fields()) != 1 || col.Fields()[0].Name() != "language" {
		t.Fatalf("unexpected fields: %+v", col.Fields())
	}
}

func TestGet_NotFound(t *testing.T) {
	repo, ms := newTestRepo(t)
	ctx := context.Background()

	ms.hgetAllFn = func(_ context.Context, _ string) (map[string]string, error) {
		return map[string]string{}, nil
	}

	_, err := repo.Get(ctx, "nonexistent")
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

// --- List ---

func TestList_HappyPath(t *testing.T) {
	repo, ms := newTestRepo(t)
	ctx := context.Background()

	ms.scanFn = func(_ context.Context, _ string) ([]string, error) {
		return []string{"vecdex:collection:alpha", "vecdex:collection:beta"}, nil
	}
	ms.hgetAllMultiFn = func(_ context.Context, keys []string) ([]map[string]string, error) {
		return []map[string]string{
			{
				"name": "alpha", "type": "json", "fields_json": "[]",
				"vector_dim": "1024", "created_at": "1700000000002",
			},
			{
				"name": "beta", "type": "json", "fields_json": "[]",
				"vector_dim": "1024", "created_at": "1700000000001",
			},
		}, nil
	}

	cols, err := repo.List(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cols) != 2 {
		t.Fatalf("expected 2 collections, got %d", len(cols))
	}
	if cols[0].Name() != "beta" {
		t.Fatalf("expected first collection to be beta (earlier), got %s", cols[0].Name())
	}
	if cols[1].Name() != "alpha" {
		t.Fatalf("expected second collection to be alpha (later), got %s", cols[1].Name())
	}
}

func TestList_Empty(t *testing.T) {
	repo, ms := newTestRepo(t)
	ctx := context.Background()

	ms.scanFn = func(_ context.Context, _ string) ([]string, error) {
		return nil, nil
	}

	cols, err := repo.List(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cols) != 0 {
		t.Fatalf("expected empty list, got %d", len(cols))
	}
}

// --- Delete ---

func TestDelete_HappyPath(t *testing.T) {
	repo, ms := newTestRepo(t)
	ctx := context.Background()

	ms.hgetAllFn = func(_ context.Context, _ string) (map[string]string, error) {
		return map[string]string{
			"name": "test-collection", "type": "json", "fields_json": "[]",
			"vector_dim": "1024", "created_at": "1700000000000",
		}, nil
	}
	ms.indexExistsFn = func(_ context.Context, _ string) (bool, error) { return true, nil }
	ms.delFn = func(_ context.Context, _ string) error { return nil }
	ms.dropIndexFn = func(_ context.Context, _ string) error { return nil }

	err := repo.Delete(ctx, "test-collection")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestDelete_NotFound(t *testing.T) {
	repo, ms := newTestRepo(t)
	ctx := context.Background()

	ms.hgetAllFn = func(_ context.Context, _ string) (map[string]string, error) {
		return map[string]string{}, nil
	}

	err := repo.Delete(ctx, "nonexistent")
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}
