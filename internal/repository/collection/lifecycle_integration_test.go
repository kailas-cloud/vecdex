package collection

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/kailas-cloud/vecdex/internal/db"
	valkeydb "github.com/kailas-cloud/vecdex/internal/db/valkey"
	"github.com/kailas-cloud/vecdex/internal/domain"
	domcol "github.com/kailas-cloud/vecdex/internal/domain/collection"
	domdoc "github.com/kailas-cloud/vecdex/internal/domain/document"
	"github.com/kailas-cloud/vecdex/internal/domain/storage"
	documentrepo "github.com/kailas-cloud/vecdex/internal/repository/document"
	documentuc "github.com/kailas-cloud/vecdex/internal/usecase/document"
)

// Lifecycle tests exercise real Valkey hashes and native transactions. Search index lifecycle is an
// in-memory stand-in here; the complete search module is covered by Docker E2E.
type lifecycleStore struct {
	*valkeydb.Store
	indexes sync.Map
}

func (s *lifecycleStore) CreateIndex(_ context.Context, def *db.IndexDefinition) error {
	if _, exists := s.indexes.LoadOrStore(def.Name, true); exists {
		return db.ErrIndexExists
	}
	return nil
}
func (s *lifecycleStore) DropIndex(_ context.Context, name string) error {
	if _, exists := s.indexes.LoadAndDelete(name); !exists {
		return db.ErrIndexNotFound
	}
	return nil
}

func (s *lifecycleStore) IndexExists(_ context.Context, name string) (bool, error) {
	_, exists := s.indexes.Load(name)
	return exists, nil
}

type pausedEmbedder struct{ entered, release chan struct{} }

func (e *pausedEmbedder) Embed(ctx context.Context, text string) (domain.EmbeddingResult, error) {
	if text == "late" {
		close(e.entered)
		select {
		case <-e.release:
		case <-ctx.Done():
			return domain.EmbeddingResult{}, fmt.Errorf("paused embedding: %w", ctx.Err())
		}
	}
	return domain.EmbeddingResult{Embedding: []float32{1, 0, 0}}, nil
}

func TestCollectionLifecycleLive(t *testing.T) {
	addr := os.Getenv("VECDEX_TEST_VALKEY_ADDR")
	if addr == "" {
		t.Skip("VECDEX_TEST_VALKEY_ADDR is not set")
	}
	raw, err := valkeydb.NewStore(valkeydb.Config{Addrs: []string{addr}, Standalone: true})
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	store := &lifecycleStore{Store: raw}
	collections := New(store, 3)
	name := fmt.Sprintf("lifecycle-%d", time.Now().UnixNano())
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	col, err := domcol.New(name, domcol.TypeText, nil, 3)
	if err != nil {
		t.Fatal(err)
	}
	if err := collections.Create(ctx, col); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = collections.Delete(context.Background(), name) }()
	embed := &pausedEmbedder{entered: make(chan struct{}), release: make(chan struct{})}
	docs := documentuc.New(documentrepo.New(store), collections, embed, embed)
	original, _ := domdoc.New("original", "old", nil, nil)
	if _, err := docs.Upsert(ctx, name, &original); err != nil {
		t.Fatal(err)
	}
	assertLateWriteRejected(t, ctx, name, col, collections, docs, embed)
}

func assertLateWriteRejected(
	t *testing.T,
	ctx context.Context,
	name string,
	old domcol.Collection,
	collections *Repo,
	docs *documentuc.Service,
	embed *pausedEmbedder,
) {
	t.Helper()
	done := make(chan error, 1)
	go func() {
		late, _ := domdoc.New("original", "late", nil, nil)
		_, err := docs.Upsert(ctx, name, &late)
		done <- err
	}()
	select {
	case <-embed.entered:
	case <-ctx.Done():
		t.Fatal("writer did not enter embedder")
	}
	if err := collections.Delete(ctx, name); err != nil {
		t.Fatal(err)
	}
	next, err := domcol.New(name, domcol.TypeText, nil, 3)
	if err != nil {
		t.Fatal(err)
	}
	if next.Generation() == old.Generation() {
		t.Fatal("recreated collection reused identity")
	}
	if err := collections.Create(ctx, next); err != nil {
		t.Fatal(err)
	}
	if _, err := docs.Get(ctx, name, "original"); !errors.Is(err, domain.ErrDocumentNotFound) {
		t.Fatalf("old document resurrected: %v", err)
	}
	fresh, _ := domdoc.New("original", "fresh", nil, nil)
	if _, err := docs.Upsert(ctx, name, &fresh); err != nil {
		t.Fatal(err)
	}
	close(embed.release)
	if err := <-done; !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("stale write accepted: %v", err)
	}
	current, err := docs.Get(ctx, name, "original")
	if err != nil || current.Content() != "fresh" {
		t.Fatalf("fresh data changed: %v %v", current, err)
	}
	count, err := docs.Count(ctx, name)
	if err != nil || count != 1 {
		t.Fatalf("count=%d err=%v", count, err)
	}
}

func (s *lifecycleStore) WaitForIndex(ctx context.Context, name string) error { return ctx.Err() }

func TestConcurrentLifecycleWorkersLeaveNoWorkAfterDeletionLive(t *testing.T) {
	raw, _, col := lifecycleFixture(t)
	store := &lifecycleStore{Store: raw}
	repo := New(store, 3)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := repo.Create(ctx, col); err != nil {
		t.Fatal(err)
	}
	layout, err := storage.NewCollection(col.Name(), col.Generation())
	if err != nil {
		t.Fatal(err)
	}
	seedLifecycleSources(t, ctx, store, col)
	var workers sync.WaitGroup
	failures := make(chan error, 3)
	for range 3 {
		workers.Go(func() {
			if err := New(store, 3).Delete(ctx, col.Name()); err != nil && !errors.Is(err, domain.ErrNotFound) {
				failures <- err
			}
		})
	}
	workers.Wait()
	close(failures)
	for err := range failures {
		t.Error(err)
	}
	assertCollectionPurged(t, ctx, store, repo, col, layout)

}

func seedLifecycleSources(t *testing.T, ctx context.Context, store *lifecycleStore, col domcol.Collection) {
	t.Helper()
	docs := documentrepo.New(store)
	for i := range 24 {
		doc, err := domdoc.New(fmt.Sprintf("source-%d", i), "source", nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		doc.SetVector([]float32{1, 0, 0})
		if _, err := docs.Upsert(domain.WithCollectionGeneration(ctx, col.Generation()), col.Name(), &doc); err != nil {
			t.Fatal(err)
		}
	}

}

func assertCollectionPurged(
	t *testing.T,
	ctx context.Context,
	store *lifecycleStore,
	repo *Repo,
	col domcol.Collection,
	layout storage.Collection,
) {
	t.Helper()
	raw := store.Store
	queues := make([]string, storage.PartitionCount)
	catalogs := make([]string, storage.PartitionCount)
	for p := range storage.PartitionCount {
		queues[p] = layout.WorkQueue(p)
		catalogs[p] = layout.Catalog(p)
	}
	for _, keys := range [][]string{queues, catalogs} {
		counts, err := raw.CatalogCounts(ctx, keys)
		if err != nil {
			t.Fatal(err)
		}
		for _, count := range counts {
			if count != 0 {
				t.Fatal("deleted generation retained reachable work or sources")
			}
		}
	}
	key := layout.Guard(0)
	if err := repo.closeGuard(ctx, key, col.Generation()); err != nil {
		t.Fatal(err)
	}
	row, err := raw.HGetAll(ctx, key)
	if err != nil || row["state"] != "purging" {
		t.Fatalf("late deleter reopened guard: %v", err)
	}
	metadata, err := raw.HGetAll(ctx, storage.Metadata(col.Name()))
	if err != nil || len(metadata) != 0 {
		t.Fatalf("deletion metadata retained: %v", err)
	}
}
