package collection

import (
	"context"
	"errors"
	"testing"

	"github.com/kailas-cloud/vecdex/internal/domain"
	domcol "github.com/kailas-cloud/vecdex/internal/domain/collection"
	"github.com/kailas-cloud/vecdex/internal/domain/storage"
)

func TestCatalogPageAdvancesPastDeletedCursorAndHiddenRows(t *testing.T) {
	repo, store := newTestRepo(t)
	ctx := context.Background()
	for _, name := range []string{"a", "b", "c"} {
		col, err := domcol.New(name, domcol.TypeText, nil, 3)
		if err != nil {
			t.Fatal(err)
		}
		if err := repo.Create(ctx, col); err != nil {
			t.Fatal(err)
		}
	}
	store.rows[storage.Metadata("a")]["state"] = "deleting"
	page, cursor, err := repo.ListPage(ctx, "", 1)
	if err != nil || len(page) != 0 || cursor == "" {
		t.Fatalf("hidden first page: %v %q %v", page, cursor, err)
	}
	delete(store.rows, storage.Metadata("a"))
	delete(store.catalogs[storage.CollectionCatalog(storage.ControlPartition("a"))], "a")
	page, next, err := repo.ListPage(ctx, cursor, 1)
	if err != nil || len(page) != 1 || page[0].Name() != "b" || next == "" {
		t.Fatalf("deleted cursor stopped page: %v %q %v", page, next, err)
	}
	page, next, err = repo.ListPage(ctx, next, 1)
	if err != nil || len(page) != 1 || page[0].Name() != "c" || next != "" {
		t.Fatalf("last page: %v %q %v", page, next, err)
	}
}

func TestInspectionAndConditionalDeletion(t *testing.T) {
	repo, store := newTestRepo(t)
	ctx := context.Background()
	absent, err := repo.Inspect(ctx, "notes")
	if err != nil || absent.State != "absent" || absent.Collection != nil {
		t.Fatalf("absent inspection: %+v %v", absent, err)
	}
	col, _ := domcol.New("notes", domcol.TypeText, nil, 3)
	if err := repo.Create(ctx, col); err != nil {
		t.Fatal(err)
	}
	for _, state := range []string{"creating", "deleting", "active"} {
		store.rows[storage.Metadata("notes")]["state"] = state
		got, err := repo.Inspect(ctx, "notes")
		if err != nil || got.State != state || got.Generation != col.Generation() {
			t.Fatalf("inspection: %+v %v", got, err)
		}
	}
	err = repo.Delete(domain.WithExpectedCollectionGeneration(ctx, "old"), "notes")
	if !errors.Is(err, domain.ErrCollectionGenerationMismatch) {
		t.Fatalf("stale delete: %v", err)
	}
	if _, err := repo.Get(ctx, "notes"); err != nil {
		t.Fatalf("stale delete removed replacement: %v", err)
	}
	if err := repo.Delete(domain.WithExpectedCollectionGeneration(ctx, col.Generation()), "notes"); err != nil {
		t.Fatal(err)
	}
}
