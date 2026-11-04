package valkey

import (
	"context"
	"crypto/rand"
	"os"
	"testing"

	"github.com/kailas-cloud/vecdex/internal/db"
)

func TestCatalogTransactionFailuresLive(t *testing.T) {
	addr := os.Getenv("VECDEX_TEST_VALKEY_ADDR")
	if addr == "" {
		t.Skip("VECDEX_TEST_VALKEY_ADDR is not set")
	}
	admin, err := NewStore(Config{Addrs: []string{addr}, Standalone: true})
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	ctx := context.Background()
	base := "vecdex:v2:{catalog-" + rand.Text() + "}"
	defer func() {
		keys, err := admin.Scan(ctx, base+"*")
		if err != nil {
			t.Error(err)
			return
		}
		admin.unlinkHashes(keys)
	}()
	original := &db.HashMutation{
		Key:           base + ":doc:stable",
		CatalogKey:    base + ":ids",
		CatalogMember: "stable",
		Replace:       true,
		Set: map[string]string{
			"__content": "original",
		},
		Children: []db.HashSetItem{{Key: base + ":chunk:old", Fields: map[string]string{"text": "old"}}},
	}
	if _, err := admin.MutateHash(ctx, original); err != nil {
		t.Fatal(err)
	}
	for _, denied := range []string{"zadd", "rename", "zrem"} {
		t.Run(denied, func(t *testing.T) { verifyCatalogRejectedCommand(t, admin, addr, base, denied) })
	}
	verifyCatalogWrongType(t, admin, base)
}

func verifyCatalogRejectedCommand(t *testing.T, admin *Store, addr, base, denied string) {
	t.Helper()
	ctx := context.Background()
	user := "catalog-fault-" + rand.Text()
	command := admin.b().Arbitrary("ACL").Args("SETUSER", user, "reset", "on", ">catalog-test-password", "~"+base+"*",
		"+@all", "-"+denied, "-acl", "-flushall", "-flushdb").Build()
	if err := admin.do(ctx, command).Error(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = admin.do(ctx, admin.b().Arbitrary("ACL").Args("DELUSER", user).Build()).Error() }()
	restricted, err := NewStore(
		Config{Addrs: []string{addr}, Standalone: true, Username: user, Password: "catalog-test-password"},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer restricted.Close()
	id := "stable"
	if denied == "rename" {
		id = "new"
	}
	mutation := &db.HashMutation{
		Key:           base + ":doc:" + id,
		CatalogKey:    base + ":ids",
		CatalogMember: id,
		Replace:       true,
		Set: map[string]string{
			"__content": "uncommitted",
		},
		Children: []db.HashSetItem{{Key: base + ":chunk:" + denied, Fields: map[string]string{"text": "new"}}},
	}
	if denied == "zrem" {
		mutation.Delete = true
		mutation.RequireExists = true
		mutation.Children = nil
	}
	if _, err := restricted.MutateHash(ctx, mutation); err == nil {
		t.Fatal("denied command unexpectedly committed")
	}
	verifyCatalogUnchanged(t, admin, base, denied)
}

func verifyCatalogWrongType(t *testing.T, store *Store, base string) {
	t.Helper()
	ctx := context.Background()
	store.unlinkHashes([]string{base + ":ids"})
	if err := store.Set(ctx, base+":ids", []byte("wrong type")); err != nil {
		t.Fatal(err)
	}
	_, err := store.MutateHash(
		ctx,
		&db.HashMutation{Key: base + ":doc:stable", CatalogKey: base + ":ids", CatalogMember: "stable",
			Replace: true, Set: map[string]string{"__content": "bad replacement"}},
	)
	if err == nil {
		t.Fatal("invalid catalog type was accepted")
	}
	parent, err := store.HGetAll(ctx, base+":doc:stable")
	if err != nil || parent["__content"] != "original" {
		t.Fatalf("wrong type damaged parent: %+v %v", parent, err)
	}
}

func verifyCatalogUnchanged(t *testing.T, admin *Store, base, denied string) {
	t.Helper()
	ctx := context.Background()
	parent, err := admin.HGetAll(ctx, base+":doc:stable")
	if err != nil || parent["__content"] != "original" || parent["__revision"] != "1" {
		t.Fatalf("parent changed: %+v %v", parent, err)
	}
	ids, err := admin.CatalogIDs(ctx, base+":ids", "", 10)
	if err != nil || len(ids) != 1 || ids[0] != "stable" {
		t.Fatalf("catalog changed: %v %v", ids, err)
	}
	if exists, err := admin.Exists(ctx, base+":chunk:old"); err != nil || !exists {
		t.Fatalf("old fragment lost: %t %v", exists, err)
	}
	if exists, err := admin.Exists(ctx, base+":chunk:"+denied); err != nil || exists {
		t.Fatalf("failed fragment leaked: %t %v", exists, err)
	}
}
