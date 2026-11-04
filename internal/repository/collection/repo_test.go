package collection

import (
	"context"
	"errors"
	"testing"

	"github.com/kailas-cloud/vecdex/internal/db"
	"github.com/kailas-cloud/vecdex/internal/domain"
	domcol "github.com/kailas-cloud/vecdex/internal/domain/collection"
)

// --- Create ---

func TestCreate_HappyPath(t *testing.T) {
	repo, ms := newTestRepo(t)
	ctx := context.Background()
	col := testCollection(t)

	ms.existsFn = func(_ context.Context, _ string) (bool, error) { return false, nil }
	ms.hsetFn = func(_ context.Context, key string, _ map[string]string) error {
		if key != "vecdex:v2:{test-collection}:meta" {
			t.Errorf("unexpected key: %s", key)
		}
		return nil
	}
	ms.createIndexFn = func(_ context.Context, def *db.IndexDefinition) error {
		if def.Name != "vecdex:v2:{test-collection}:g:fixture:index" {
			t.Errorf("unexpected index name: %s", def.Name)
		}
		return nil
	}

	err := repo.Create(ctx, col)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCreate_HappyPath_WithTextSearch(t *testing.T) {
	repo, ms := newTestRepo(t)
	ctx := context.Background()
	col := testCollection(t)

	ms.supportsTextSearch = true
	ms.existsFn = func(_ context.Context, _ string) (bool, error) { return false, nil }
	ms.hsetFn = func(_ context.Context, _ string, _ map[string]string) error { return nil }
	ms.createIndexFn = func(_ context.Context, def *db.IndexDefinition) error {
		if !hasIndexField(def.Fields, "__content", db.IndexFieldText) {
			t.Fatalf("expected __content TEXT field in index definition: %+v", def.Fields)
		}
		if !hasIndexField(def.Fields, domcol.SystemParentDocID, db.IndexFieldTag) {
			t.Fatalf("expected %s TAG field in index definition: %+v", domcol.SystemParentDocID, def.Fields)
		}
		if !hasIndexField(def.Fields, "__n:"+domcol.SystemChunkIndex, db.IndexFieldNumeric) {
			t.Fatalf("expected %s NUMERIC field in index definition: %+v", domcol.SystemChunkIndex, def.Fields)
		}
		return nil
	}

	err := repo.Create(ctx, col)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCreate_AlreadyExists(t *testing.T) {
	repo, store := newTestRepo(t)
	store.mutateFn = func(_ context.Context, m *db.HashMutation) (db.HashMutationResult, error) {
		if !m.RequireAbsent {
			t.Fatal("metadata reservation was not conditional")
		}
		return db.HashMutationResult{}, domain.ErrAlreadyExists
	}
	if err := repo.Create(context.Background(), testCollection(t)); !errors.Is(err, domain.ErrAlreadyExists) {
		t.Fatalf("error %v", err)
	}
}

func TestCreate_ReservationError(t *testing.T) {
	repo, store := newTestRepo(t)
	store.mutateFn = func(context.Context, *db.HashMutation) (db.HashMutationResult, error) {
		return db.HashMutationResult{}, errors.New("connection lost")
	}
	if err := repo.Create(context.Background(), testCollection(t)); err == nil {
		t.Fatal("expected reservation error")
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
	ms.mutateFn = func(_ context.Context, m *db.HashMutation) (db.HashMutationResult, error) {
		if m.Delete {
			delCalled = true
			if m.GuardState != "creating" || m.ExpectedRevision == nil {
				t.Fatal("unsafe rollback")
			}
		}
		return db.HashMutationResult{Revision: 1, Created: true}, nil
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
		if key != "vecdex:v2:{test-collection}:meta" {
			t.Errorf("unexpected key: %s", key)
		}
		return map[string]string{
			"name":        "test-collection",
			"type":        "json",
			"fields_json": `[{"name":"language","type":"tag"}]`,
			"vector_dim":  "1024",
			"generation":  "fixture", "created_at": "1700000000000",
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
		return []string{"vecdex:v2:{alpha}:meta", "vecdex:v2:{beta}:meta"}, nil
	}
	ms.hgetAllMultiFn = func(_ context.Context, keys []string) ([]map[string]string, error) {
		return []map[string]string{
			{
				"name": "alpha", "type": "json", "fields_json": "[]",
				"vector_dim": "1024", "generation": "fixture", "created_at": "1700000000002",
			},
			{
				"name": "beta", "type": "json", "fields_json": "[]",
				"vector_dim": "1024", "generation": "fixture", "created_at": "1700000000001",
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
			"vector_dim": "1024", "generation": "fixture", "created_at": "1700000000000",
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

func hasIndexField(fields []db.IndexField, name string, typ db.IndexFieldType) bool {
	for index := range fields {
		field := &fields[index]
		if field.Name == name && field.Type == typ {
			return true
		}
	}
	return false
}

func TestDeleteCleansOnlyCapturedGeneration(t *testing.T) {
	repo, store := newTestRepo(t)
	store.hgetAllFn = func(context.Context, string) (map[string]string, error) {
		return map[string]string{"generation": "old", "state": "active", "__revision": "2"}, nil
	}
	store.scanFn = func(_ context.Context, pattern string) ([]string, error) {
		if pattern != "vecdex:v2:{notes}:g:old:*" {
			t.Fatalf("unsafe cleanup pattern %s", pattern)
		}
		return []string{"vecdex:v2:{notes}:g:old:doc:a", "vecdex:v2:{notes}:g:old:pending:a"}, nil
	}
	removed := 0
	store.delFn = func(context.Context, string) error { removed++; return nil }
	store.dropIndexFn = func(_ context.Context, name string) error {
		if name != "vecdex:v2:{notes}:g:old:index" {
			t.Fatalf("wrong index %s", name)
		}
		return nil
	}
	calls := 0
	store.mutateFn = func(_ context.Context, m *db.HashMutation) (db.HashMutationResult, error) {
		calls++
		if m.GuardGeneration != "old" {
			t.Fatal("generation guard missing")
		}
		if calls == 1 && m.Set["state"] != "deleting" {
			t.Fatal("write barrier missing")
		}
		if m.Delete {
			return db.HashMutationResult{}, domain.ErrNotFound
		} // Another request already finished and recreated it.
		return db.HashMutationResult{Revision: 3}, nil
	}
	if err := repo.Delete(context.Background(), "notes"); err != nil {
		t.Fatal(err)
	}
	if removed != 2 || calls != 2 {
		t.Fatalf("cleanup removed=%d calls=%d", removed, calls)
	}
}
func TestInactiveCollectionsAreHidden(t *testing.T) {
	repo, store := newTestRepo(t)
	for _, state := range []string{"creating", "deleting"} {
		store.hgetAllFn = func(context.Context, string) (map[string]string, error) {
			return map[string]string{"state": state}, nil
		}
		if _, err := repo.Get(context.Background(), "notes"); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("state %s: %v", state, err)
		}
	}
}
