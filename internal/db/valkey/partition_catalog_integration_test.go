package valkey

import (
	"context"
	"crypto/rand"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kailas-cloud/vecdex/internal/db"
	"github.com/kailas-cloud/vecdex/internal/domain/storage"
)

// This fixture tests v3 key placement and native commits, independently of Search.
// The full app/Search reshard suite must also pass before switching runtime layouts.
func TestPartitionCatalogNativeLive(t *testing.T) {
	cluster := os.Getenv("VECDEX_TEST_VALKEY_CLUSTER_ADDRS")
	addresses := []string{os.Getenv("VECDEX_TEST_VALKEY_ADDR")}
	if cluster != "" {
		addresses = strings.Split(cluster, ",")
	}
	if addresses[0] == "" {
		t.Skip("isolated Valkey address is required")
	}
	store, err := NewStore(Config{Addrs: addresses, Standalone: cluster == ""})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	layout, err := storage.NewCollection("partition-test", rand.Text())
	if err != nil {
		t.Fatal(err)
	}
	catalogs := make([]string, storage.PartitionCount)
	guards := make([]db.HashSetItem, storage.PartitionCount)
	cleanup := make([]string, 0, 3*(storage.PartitionCount+300))
	for p := range storage.PartitionCount {
		catalogs[p] = layout.Catalog(p)
		guards[p] = db.HashSetItem{Key: layout.Guard(p), Fields: map[string]string{"generation": "test", "state": "active"}}
		cleanup = append(cleanup, catalogs[p], guards[p].Key, layout.WorkQueue(p))
	}
	defer func() {
		cleanupCtx, finish := context.WithTimeout(context.Background(), 10*time.Second)
		defer finish()
		for _, key := range cleanup {
			if err := store.Del(cleanupCtx, key); err != nil {
				t.Error(err)
			}
		}
	}()
	if err := store.HSetMulti(ctx, guards); err != nil {
		t.Fatal(err)
	}
	want := make([]string, 300)
	for i := range want {
		id := fmt.Sprintf("source-%04d", i)
		want[i] = id
		cleanup = append(cleanup, layout.Document(id), layout.Fragment(id, "version", 0))
		result, err := store.MutateHash(ctx, &db.HashMutation{
			Key: layout.Document(id), StagingKey: layout.Staging(id, "prepare"),
			WorkQueueKey: layout.WorkQueue(storage.Partition(id)),
			CatalogKey:   layout.Catalog(storage.Partition(id)), CatalogMember: id,
			GuardKey: layout.Guard(storage.Partition(id)), GuardGeneration: "test", GuardState: "active",
			Set: map[string]string{"__content": "whole source " + id}, Replace: true,
			Children: []db.HashSetItem{{Key: layout.Fragment(id, "version", 0), Fields: map[string]string{"text": id}}},
		})
		if err != nil {
			t.Fatalf("native partition commit %s: %v", id, err)
		}
		cleanup = append(cleanup, workKey(layout.WorkQueue(storage.Partition(id)), result.OperationID))
	}
	verifyNativePartitionCatalogs(t, ctx, store, catalogs, want)
	if cluster != "" {
		verifySourcePrimaries(t, ctx, store, layout)
	}
}

func verifyNativePartitionCatalogs(t *testing.T, ctx context.Context, store *Store, catalogs, want []string) {
	t.Helper()
	counts, err := store.CatalogCounts(ctx, catalogs)
	if err != nil {
		t.Fatal(err)
	}
	total, occupied := 0, 0
	for _, count := range counts {
		total += count
		if count > 0 {
			occupied++
		}
	}
	if total != len(want) || occupied < 100 {
		t.Fatalf("total=%d occupied=%d", total, occupied)
	}
	var got []string
	after := ""
	for {
		page, err := db.ReadCatalogPage(ctx, store, catalogs, after, 23)
		if err != nil {
			t.Fatal(err)
		}
		if len(page) == 0 {
			break
		}
		got = append(got, page...)
		after = page[len(page)-1]
	}
	if !slices.Equal(got, want) {
		t.Fatal("native partition catalogs skipped or reordered source IDs")
	}
}

func verifySourcePrimaries(t *testing.T, ctx context.Context, store *Store, layout storage.Collection) {
	t.Helper()
	primaries := 0
	for address, node := range store.client.Nodes() {
		info, err := node.Do(ctx, node.B().Info().Section("replication").Build()).ToString()
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(info, "role:master") {
			continue
		}
		primaries++
		count := 0
		var cursor uint64
		for {
			// Node-local cursors are only used to verify actual physical placement.
			page, err := node.Do(ctx, node.B().Scan().Cursor(cursor).Match(layout.Base()+":doc:*").Build()).AsScanEntry()
			if err != nil {
				t.Fatal(err)
			}
			count += len(page.Elements)
			cursor = page.Cursor
			if cursor == 0 {
				break
			}
		}
		t.Logf("primary %s contains %d source documents", address, count)
		if count == 0 {
			t.Fatalf("collection did not reach primary %s", address)
		}
	}
	if primaries < 3 {
		t.Fatalf("cluster fixture requires three primaries, found %d", primaries)
	}
}
