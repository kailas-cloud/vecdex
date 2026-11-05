package valkey

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/kailas-cloud/vecdex/internal/db"
	"github.com/kailas-cloud/vecdex/internal/domain"
	"github.com/kailas-cloud/vecdex/internal/domain/storage"
	"github.com/redis/rueidis"
)

func TestExpiredSourceRecreationBeforeCleanupLive(t *testing.T) {
	store, _, layout, mutation := workFixture(t)
	ctx := context.Background()
	mutation.DeadlineKey = layout.Deadlines(storage.Partition("source"))
	mutation.ExpiryMarkerKey = layout.ExpiryMarker("source")
	mutation.ExpiresAt = time.Now().Add(-time.Second).UnixMilli()
	old, err := store.MutateHash(ctx, mutation)
	if err != nil || !old.Created {
		t.Fatalf("first expiring commit: %+v %v", old, err)
	}
	assertDeadlineCounts(t, store, mutation.DeadlineKey, 0)
	ids, err := store.ExpiryIDs(ctx, mutation.DeadlineKey, time.Now().UnixMilli(), 8)
	if err != nil || len(ids) != 1 || ids[0] != "source" {
		t.Fatalf("expired source was not discoverable: %v %v", ids, err)
	}
	mutation.ExpectedVersion = &old.Version
	if _, err := store.MutateHash(ctx, mutation); !errors.Is(err, domain.ErrRevisionConflict) {
		t.Fatalf("expired source accepted its former version: %v", err)
	}
	mutation.ExpectedVersion, mutation.ExpiresAt = nil, 0
	mutation.Children[0].Key = layout.Fragment("source", "recreated", 0)
	fresh, err := store.MutateHash(ctx, mutation)
	if err != nil || !fresh.Created || fresh.Revision != 1 || fresh.Version == old.Version {
		t.Fatalf("expired source did not start a fresh incarnation: %+v %v", fresh, err)
	}
	assertDeadlineCounts(t, store, mutation.DeadlineKey, 1)
	staleCleanup := *mutation
	staleCleanup.Delete, staleCleanup.ExpectedVersion = true, &old.Version
	staleCleanup.ExpiredBefore = time.Now().UnixMilli()
	if _, err := store.MutateHash(ctx, &staleCleanup); !errors.Is(err, domain.ErrRevisionConflict) {
		t.Fatalf("old expiry cleanup deleted a new incarnation: %v", err)
	}
	row, err := store.HGetAll(ctx, mutation.Key)
	if err != nil || row["__source_version"] != fresh.Version {
		t.Fatalf("new incarnation was lost: %v %v", row, err)
	}
}

func assertDeadlineCounts(t *testing.T, store *Store, key string, want int) {
	t.Helper()
	counts, err := store.LiveCounts(context.Background(), []string{key}, time.Now().UnixMilli())
	if err != nil || len(counts) != 1 || counts[0] != want {
		t.Fatalf("live count: %v want %d, err=%v", counts, want, err)
	}
}

func TestExpiredDeletionRemovesItsDeadlineMembershipLive(t *testing.T) {
	store, _, layout, mutation := workFixture(t)
	ctx := context.Background()
	mutation.DeadlineKey = layout.Deadlines(storage.Partition("source"))
	mutation.ExpiryMarkerKey = layout.ExpiryMarker("source")
	mutation.ExpiresAt = time.Now().Add(-time.Second).UnixMilli()
	written, err := store.MutateHash(ctx, mutation)
	if err != nil {
		t.Fatal(err)
	}
	mutation.Delete, mutation.ExpectedVersion = true, &written.Version
	mutation.ExpiredBefore = time.Now().UnixMilli()
	if _, err := store.MutateHash(ctx, mutation); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{mutation.Key, mutation.DeadlineKey, mutation.CatalogKey, mutation.Children[0].Key} {
		if exists, err := store.Exists(ctx, key); err != nil || exists {
			t.Fatalf("expiry cleanup retained %s: exists=%v err=%v", key, exists, err)
		}
	}
	assertDeadlineCounts(t, store, mutation.DeadlineKey, 0)
	if count, err := store.CatalogCount(ctx, mutation.WorkQueueKey); err != nil || count != 0 {
		t.Fatalf("completed expiry cleanup remained queued: %d %v", count, err)
	}
}

var _ db.ExpiryStore = (*Store)(nil)

func TestDeadlineExpirationFencesAlreadyWatchedCommitLive(t *testing.T) {
	store, _, layout, mutation := workFixture(t)
	ctx := context.Background()
	mutation.DeadlineKey = layout.Deadlines(storage.Partition("source"))
	mutation.ExpiryMarkerKey = layout.ExpiryMarker("source")
	mutation.ExpiresAt = time.Now().Add(time.Second).UnixMilli()
	written, err := store.MutateHash(ctx, mutation)
	if err != nil {
		t.Fatal(err)
	}
	client, release := store.client.Dedicate()
	defer release()
	if err := watchKeys(ctx, client, mutation.Key, mutation.ExpiryMarkerKey); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Until(time.UnixMilli(mutation.ExpiresAt)) + time.Millisecond)
	_, committed, err := execTransaction(ctx, client, []rueidis.Completed{
		client.B().Hset().Key(mutation.Key).FieldValue().FieldValue("illegal-extension", "true").Build(),
	})
	if err != nil || committed {
		t.Fatalf("deadline expiry did not abort an already-watched commit: committed=%v %v", committed, err)
	}
	row, err := store.HGetAll(ctx, mutation.Key)
	if err != nil || row["__source_version"] != written.Version || row["illegal-extension"] != "" {
		t.Fatalf("expired manifest was lost or late commit was published: %v %v", row, err)
	}
}
