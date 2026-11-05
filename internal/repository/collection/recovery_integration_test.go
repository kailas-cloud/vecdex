package collection

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/redis/rueidis"

	"github.com/kailas-cloud/vecdex/internal/db"
	valkeydb "github.com/kailas-cloud/vecdex/internal/db/valkey"
	"github.com/kailas-cloud/vecdex/internal/domain"
	domcol "github.com/kailas-cloud/vecdex/internal/domain/collection"
	"github.com/kailas-cloud/vecdex/internal/domain/storage"
)

var errInterruptedLifecycle = errors.New("simulated process interruption")

type interruptedLifecycleStore struct {
	*valkeydb.Store
	create bool
	drop   bool
}

func (s *interruptedLifecycleStore) CreateIndex(ctx context.Context, index *db.IndexDefinition) error {
	if s.create {
		return errInterruptedLifecycle
	}
	return s.Store.CreateIndex(ctx, index)
}
func (s *interruptedLifecycleStore) DropIndex(ctx context.Context, name string) error {
	if s.drop {
		return errInterruptedLifecycle
	}
	return s.Store.DropIndex(ctx, name)
}

func lifecycleFixture(t *testing.T) (*valkeydb.Store, valkeydb.Config, domcol.Collection) {
	t.Helper()
	addr := os.Getenv("VECDEX_TEST_VALKEY_ADDR")
	if addr == "" {
		t.Skip("isolated Valkey Search address required")
	}
	cfg := valkeydb.Config{Addrs: []string{addr}, Standalone: true}
	store, err := valkeydb.NewStore(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	col, err := domcol.New("recovery-"+rand.Text(), domcol.TypeText, nil, 3)
	if err != nil {
		t.Fatal(err)
	}
	return store, cfg, col
}

func TestBackgroundLifecycleResumesAfterClientRestartLive(t *testing.T) {
	store, cfg, col := lifecycleFixture(t)
	interrupted := New(&interruptedLifecycleStore{Store: store, create: true}, 3)
	if err := interrupted.Create(context.Background(), col); !errors.Is(err, errInterruptedLifecycle) {
		t.Fatalf("create=%v", err)
	}
	store.Close()
	restarted, err := valkeydb.NewStore(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	repo := New(restarted, 3)
	defer func() { _ = repo.Delete(context.Background(), col.Name()) }()
	stop := repo.StartMaintenance(context.Background(), func(err error) { t.Log(err) })
	defer stop()
	waitLifecycle(t, func() bool { _, err := repo.Get(context.Background(), col.Name()); return err == nil })
	stop()
	layout, _ := storage.NewCollection(col.Name(), col.Generation())
	if exists, err := restarted.IndexExists(context.Background(), layout.Index()); err != nil || !exists {
		t.Fatalf("recovered index exists=%v err=%v", exists, err)
	}
	interrupted = New(&interruptedLifecycleStore{Store: restarted, drop: true}, 3)
	if err := interrupted.Delete(context.Background(), col.Name()); !errors.Is(err, errInterruptedLifecycle) {
		t.Fatalf("delete=%v", err)
	}
	stop = repo.StartMaintenance(context.Background(), func(err error) { t.Log(err) })
	defer stop()
	waitLifecycle(t, func() bool {
		rows, err := restarted.HGetAll(context.Background(), storage.Metadata(col.Name()))
		return err == nil && len(rows) == 0
	})
	if exists, err := restarted.IndexExists(context.Background(), layout.Index()); err != nil || exists {
		t.Fatalf("deleted index exists=%v err=%v", exists, err)
	}
}

func TestDeletionRemovesUnpublishedRegistrationLive(t *testing.T) {
	store, _, col := lifecycleFixture(t)
	repo := New(store, 3)
	ctx := context.Background()
	if err := repo.Create(ctx, col); err != nil {
		t.Fatal(err)
	}
	layout, _ := storage.NewCollection(col.Name(), col.Generation())
	// A registered, not-yet-due journal models a stopped source writer. Its
	// temporary registration cannot have published payload before conversion.
	queue := layout.WorkQueue(0)
	id := "stopped-registration"
	if err := store.SetWithTTL(ctx, queue+":operation:"+id, []byte("registering"), 10*time.Minute); err != nil {
		t.Fatal(err)
	}
	client, err := rueidis.NewClient(rueidis.ClientOption{
		InitAddress: []string{os.Getenv("VECDEX_TEST_VALKEY_ADDR")}, ForceSingleClient: true, DisableCache: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if err := client.Do(ctx, client.B().Zadd().Key(queue).ScoreMember().
		ScoreMember(float64(time.Now().Add(10*time.Minute).UnixMilli()), id).Build()).Error(); err != nil {
		t.Fatal(err)
	}
	// Native queued work is produced by a guarded mutation, not a keyspace scan.
	if _, err := store.MutateHash(ctx, &db.HashMutation{
		Key: layout.Document("one"), StagingKey: layout.Staging("one", "prepare"),
		CatalogKey: layout.Catalog(storage.Partition("one")), CatalogMember: "one",
		WorkQueueKey: layout.WorkQueue(storage.Partition("one")), Replace: true,
		GuardKey: layout.Guard(storage.Partition("one")), GuardGeneration: col.Generation(), GuardState: "active",
		Set: map[string]string{"__content": "whole source"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := repo.Delete(ctx, col.Name()); err != nil {
		t.Fatal(err)
	}
	if exists, err := store.Exists(ctx, queue+":operation:"+id); err != nil || exists {
		t.Fatalf("unpublished registration exists=%v err=%v", exists, err)
	}
	if _, err := repo.Get(ctx, col.Name()); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("deleted collection visible")
	}
}

func waitLifecycle(t *testing.T, ready func() bool) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for !ready() {
		if time.Now().After(deadline) {
			t.Fatal("background lifecycle did not converge")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestBackgroundWorkerFindsAbandonedDocumentJournalLive(t *testing.T) {
	store, cfg, col := lifecycleFixture(t)
	repo := New(store, 3)
	ctx := context.Background()
	if err := repo.Create(ctx, col); err != nil {
		t.Fatal(err)
	}
	layout, _ := storage.NewCollection(col.Name(), col.Generation())
	id := ""
	for i := 0; ; i++ {
		candidate := fmt.Sprintf("source-%d", i)
		if storage.Partition(candidate) < maintenancePartitionBlock {
			id = candidate
			break
		}
	}
	queue := layout.WorkQueue(storage.Partition(id))
	operation := "abandoned"
	child, stage := layout.Fragment(id, operation, 0), layout.Staging(id, operation)
	deadline := time.Now().Add(-time.Minute).UnixMilli()
	payload, err := json.Marshal(map[string]any{
		"id": operation, "state": "preparing", "parent": layout.Document(id), "staging": stage,
		"new": []string{child, stage}, "deadline": deadline,
		"result": db.HashMutationResult{OperationID: operation, Revision: 1, Created: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	// A persisted crash checkpoint after one prepare batch, before publication.
	journal := queue + ":operation:" + operation
	if err := store.HSet(ctx, journal, map[string]string{
		"payload": string(payload), "state": "preparing", "progress": "0", "committed": "false",
		"deadline": strconv.FormatInt(deadline, 10),
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.HSet(ctx, child, map[string]string{"__prepared_by": operation, "__content": "abandoned"}); err != nil {
		t.Fatal(err)
	}
	client, err := rueidis.NewClient(rueidis.ClientOption{
		InitAddress: cfg.Addrs, ForceSingleClient: true, DisableCache: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	command := client.B().Zadd().Key(queue).ScoreMember().ScoreMember(float64(deadline), operation).Build()
	if err := client.Do(ctx, command).Error(); err != nil {
		t.Fatal(err)
	}
	client.Close()
	store.Close()
	restarted, err := valkeydb.NewStore(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	repo = New(restarted, 3)
	defer func() { _ = repo.Delete(context.Background(), col.Name()) }()
	stop := repo.StartMaintenance(context.Background(), func(err error) { t.Log(err) })
	defer stop()
	waitLifecycle(t, func() bool {
		exists, err := restarted.Exists(ctx, child)
		count, countErr := restarted.CatalogCount(ctx, queue)
		return err == nil && countErr == nil && !exists && count == 0
	})
}
