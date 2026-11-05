package valkey

import (
	"context"
	"crypto/rand"
	"os"
	"testing"

	"github.com/kailas-cloud/vecdex/internal/db"
)

func TestNativeMutationsWithScriptingDeniedLive(t *testing.T) {
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
	id := rand.Text()
	base := "vecdex:v2:{no-scripting-" + id + "}"
	user := "vecdex-native-" + id
	command := admin.b().Arbitrary("ACL").Args("SETUSER", user, "reset", "on", ">native-test-password", "~"+base+"*",
		"+@all", "-eval", "-evalsha", "-eval_ro", "-evalsha_ro", "-script", "-fcall", "-fcall_ro", "-function",
		"-flushdb", "-flushall", "-acl").Build()
	if err := admin.do(ctx, command).Error(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = admin.do(ctx, admin.b().Arbitrary("ACL").Args("DELUSER", user).Build()).Error() }()
	restricted, err := NewStore(
		Config{Addrs: []string{addr}, Standalone: true, Username: user, Password: "native-test-password"},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer restricted.Close()
	defer func() {
		keys, err := admin.ScanForTest(ctx, base+"*")
		if err != nil {
			t.Error(err)
			return
		}
		admin.unlinkHashes(keys)
	}()
	verifyNoScriptingDocument(t, restricted, base)
	verifyNoScriptingBudget(t, restricted, base)
}

func verifyNoScriptingDocument(t *testing.T, store *Store, base string) {
	t.Helper()
	ctx := context.Background()
	mutation := &db.HashMutation{
		CatalogKey:    base + ":ids",
		CatalogMember: "source",
		Key:           base + ":doc",
		Replace:       true,
		Set:           map[string]string{"__content": "full source"},
		Children: []db.HashSetItem{
			{Key: base + ":chunk:one", Fields: map[string]string{"__content": "source span"}},
		},
	}
	result, err := store.MutateHash(ctx, mutation)
	if err != nil || !result.Created || result.Revision != 1 {
		t.Fatalf("native create: %+v %v", result, err)
	}
	if _, err := store.MutateHash(
		ctx,
		&db.HashMutation{
			Key:           mutation.Key,
			CatalogKey:    base + ":ids",
			CatalogMember: "source",
			Delete:        true,
			RequireExists: true,
		},
	); err != nil {
		t.Fatal(err)
	}
	if exists, err := store.Exists(ctx, mutation.Children[0].Key); err != nil || exists {
		t.Fatalf("fragment not removed: %t %v", exists, err)
	}
}

func verifyNoScriptingBudget(t *testing.T, store *Store, base string) {
	t.Helper()
	ctx := context.Background()
	command := &db.BudgetCommand{Keys: []string{base + ":day", base + ":month", base + ":total", base + ":receipt"},
		Action: "reserve", Amount: 10, DailyLimit: 20, MonthlyLimit: 30}
	if _, err := store.Budget(ctx, command); err != nil {
		t.Fatal(err)
	}
	command.Action = "complete"
	command.Tokens = 4
	snapshot, err := store.Budget(ctx, command)
	if err != nil || snapshot.Day.Tokens != 4 || snapshot.Day.Reserved != 0 || snapshot.Total.Requests != 1 {
		t.Fatalf("native accounting: %+v %v", snapshot, err)
	}
	read, err := store.BudgetSnapshots(ctx, [][]string{command.Keys[:3]})
	if err != nil || len(read) != 1 || read[0] != snapshot {
		t.Fatalf("native accounting snapshot: %+v %v", read, err)
	}
}
