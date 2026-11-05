package collection

import (
	"context"
	"errors"
	"testing"

	"github.com/kailas-cloud/vecdex/internal/db"
	"github.com/kailas-cloud/vecdex/internal/domain"
	domcol "github.com/kailas-cloud/vecdex/internal/domain/collection"
	"github.com/kailas-cloud/vecdex/internal/domain/storage"
)

func TestCreateActivatesAllPartitionsAndOneDistributedIndex(t *testing.T) {
	repo, store := newTestRepo(t)
	col := testCollection(t)
	store.supportsTextSearch = true
	if err := repo.Create(context.Background(), col); err != nil {
		t.Fatal(err)
	}
	layout, _ := storage.NewCollection(col.Name(), col.Generation())
	for p := range storage.PartitionCount {
		guard := store.rows[layout.Guard(p)]
		if guard["generation"] != col.Generation() || guard["state"] != "active" {
			t.Fatalf("inactive guard %d", p)
		}
	}
	index := store.indexes[layout.Index()]
	if index == nil || len(index.Prefixes) != 1 || index.Prefixes[0] != layout.ChunkPrefix() {
		t.Fatal("wrong distributed index")
	}
	if !hasIndexField(index.Fields, "__content", db.IndexFieldText) {
		t.Fatal("text search field missing")
	}
	if !hasIndexField(index.Fields, "__parent_id", db.IndexFieldTag) {
		t.Fatal("parent filter missing")
	}
	current, err := repo.Get(context.Background(), col.Name())
	if err != nil || current.Generation() != col.Generation() {
		t.Fatalf("current=%+v err=%v", current, err)
	}
	queue := storage.LifecycleQueue(storage.ControlPartition(col.Name()))
	if n, _ := store.CatalogCount(context.Background(), queue); n != 0 {
		t.Fatal("completed create remained queued")
	}
	if err := repo.Create(context.Background(), col); !errors.Is(err, domain.ErrAlreadyExists) {
		t.Fatalf("duplicate create: %v", err)
	}
}

func TestBuildIndexContainsOnlyDeclaredMetadata(t *testing.T) {
	index, err := buildIndex("notes", nil, 3, true, HNSWConfig{M: 16, EFConstruct: 100})
	if err != nil {
		t.Fatal(err)
	}
	expected := []string{"__parent_id", "__expires_at", "__content", "__vector"}
	if len(index.Fields) != len(expected) {
		t.Fatalf("unexpected implicit fields: %+v", index.Fields)
	}
	for i, name := range expected {
		if index.Fields[i].Name != name {
			t.Fatalf("unexpected field: %+v", index.Fields[i])
		}
	}
}

func TestCanceledCreationIsRecoveredFromPersistentLifecycle(t *testing.T) {
	repo, store := newTestRepo(t)
	col := testCollection(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store.createIndexFn = func(context.Context, *db.IndexDefinition) error { cancel(); return context.Canceled }
	if err := repo.Create(ctx, col); !errors.Is(err, context.Canceled) {
		t.Fatalf("create=%v", err)
	}
	if store.rows[storage.Metadata(col.Name())]["state"] != "creating" {
		t.Fatal("lost creating metadata")
	}
	if _, err := repo.Get(context.Background(), col.Name()); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("partial create exposed")
	}
	store.createIndexFn = nil
	if _, err := repo.RecoverLifecycle(context.Background(), "", 8); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Get(context.Background(), col.Name()); err != nil {
		t.Fatal(err)
	}
}

func TestDeleteClosesEveryGuardBeforeCleaningData(t *testing.T) {
	repo, store := newTestRepo(t)
	ctx := context.Background()
	col := testCollection(t)
	if err := repo.Create(ctx, col); err != nil {
		t.Fatal(err)
	}
	layout, _ := storage.NewCollection(col.Name(), col.Generation())
	id := "source"
	p := storage.Partition(id)
	store.rows[layout.Document(id)] = map[string]string{"__revision": "1"}
	store.member(layout.Catalog(p), id, true)
	store.mutateFn = func(ctx context.Context, m *db.HashMutation) (db.HashMutationResult, error) {
		if m.Key == layout.Document(id) && m.Delete {
			for p := range storage.PartitionCount {
				if store.rows[layout.Guard(p)]["state"] != "deleting" {
					t.Fatal("cleanup before guard barrier")
				}
			}
		}
		return store.applyMutation(ctx, m)
	}
	failed := errors.New("coordinator unavailable")
	store.dropIndexFn = func(context.Context, string) error { return failed }
	if err := repo.Delete(ctx, col.Name()); !errors.Is(err, failed) {
		t.Fatal(err)
	}
	if store.rows[storage.Metadata(col.Name())]["state"] != "deleting" {
		t.Fatal("interrupted delete lost its job")
	}
	store.dropIndexFn = nil
	if _, err := repo.RecoverLifecycle(ctx, "", 8); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Get(ctx, col.Name()); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("deleted collection is visible")
	}
	if len(store.indexes) != 0 {
		t.Fatal("index remained")
	}
	if err := repo.activateGuard(ctx, layout.Guard(p), col.Generation()); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("late creator reopened guard")
	}
}

func TestListUsesControlCatalogs(t *testing.T) {
	repo, _ := newTestRepo(t)
	ctx := context.Background()
	for _, name := range []string{"beta", "alpha"} {
		col := domcol.Reconstruct(name, domcol.TypeText, nil, 4, 1, 1).WithGeneration("fixture")
		if err := repo.Create(ctx, col); err != nil {
			t.Fatal(err)
		}
	}
	cols, err := repo.List(ctx)
	if err != nil || len(cols) != 2 || cols[0].Name() != "alpha" || cols[1].Name() != "beta" {
		t.Fatalf("list=%v err=%v", cols, err)
	}
}

func TestMissingAndInactiveCollections(t *testing.T) {
	repo, store := newTestRepo(t)
	ctx := context.Background()
	if err := repo.Delete(ctx, "missing"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("delete=%v", err)
	}
	for _, state := range []string{"creating", "deleting"} {
		store.rows[storage.Metadata("notes")] = map[string]string{"state": state}
		if _, err := repo.Get(ctx, "notes"); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("state %s: %v", state, err)
		}
	}
}

func TestReservationErrorDoesNotCreateIndex(t *testing.T) {
	repo, store := newTestRepo(t)
	failure := errors.New("connection lost")
	store.mutateFn = func(context.Context, *db.HashMutation) (db.HashMutationResult, error) {
		return db.HashMutationResult{}, failure
	}
	if err := repo.Create(context.Background(), testCollection(t)); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	if len(store.indexes) != 0 {
		t.Fatal("index created without registered metadata")
	}
}

func hasIndexField(fields []db.IndexField, name string, kind db.IndexFieldType) bool {
	for i := range fields {
		if fields[i].Name == name && fields[i].Type == kind {
			return true
		}
	}
	return false
}

func TestCreateWaitsForEveryIndexPartitionBeforePublishing(t *testing.T) {
	repo, store := newTestRepo(t)
	failure := errors.New("one primary is still backfilling")
	store.waitForIndexFn = func(context.Context, string) error { return failure }
	col := testCollection(t)
	if err := repo.Create(context.Background(), col); !errors.Is(err, failure) {
		t.Fatalf("create err=%v", err)
	}
	if _, err := repo.Get(context.Background(), col.Name()); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("published incomplete collection: %v", err)
	}
	store.waitForIndexFn = nil
	if _, err := repo.RecoverLifecycle(context.Background(), "", 8); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Get(context.Background(), col.Name()); err != nil {
		t.Fatal(err)
	}
}

func TestConcurrentDeleteDoesNotTargetRecreatedCollection(t *testing.T) {
	repo, store := newTestRepo(t)
	ctx := context.Background()
	original := testCollection(t)
	if err := repo.Create(ctx, original); err != nil {
		t.Fatal(err)
	}
	replacement := original.WithGeneration("replacement")
	intercepted := false
	store.mutateFn = func(ctx context.Context, m *db.HashMutation) (db.HashMutationResult, error) {
		if !intercepted && m.Key == storage.Metadata(original.Name()) && m.Set["state"] == "deleting" {
			intercepted = true
			if err := repo.Delete(ctx, original.Name()); err != nil {
				t.Fatal(err)
			}
			if err := repo.Create(ctx, replacement); err != nil {
				t.Fatal(err)
			}
		}
		return store.applyMutation(ctx, m)
	}
	if err := repo.Delete(ctx, original.Name()); err != nil {
		t.Fatal(err)
	}
	current, err := repo.Get(ctx, original.Name())
	if err != nil || current.Generation() != replacement.Generation() {
		t.Fatalf("deleted recreated generation: %v", err)
	}
}
