package valkey

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kailas-cloud/vecdex/internal/db"
	"github.com/kailas-cloud/vecdex/internal/domain"
)

// TestAtomicHashLive verifies native WATCH transactions against a real Valkey.
// It uses unique keys and never flushes the database.
func TestAtomicHashLive(t *testing.T) {
	addr := os.Getenv("VECDEX_TEST_VALKEY_ADDR")
	if addr == "" {
		t.Skip("VECDEX_TEST_VALKEY_ADDR is not set")
	}
	s, err := NewStore(Config{Addrs: []string{addr}, Standalone: true})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	base := fmt.Sprintf("vecdex:v2:{atomic-%d}", time.Now().UnixNano())
	key := base + ":doc:1"
	stage := base + ":pending:1"
	defer func() {
		for _, suffix := range []string{":doc:1", ":pending:1", ":chunk:old", ":chunk:new", ":chunk:failed"} {
			_ = s.Del(ctx, base+suffix)
		}
	}()
	m := &db.HashMutation{
		Key:        key,
		StagingKey: stage,
		Replace:    true,
		Set:        map[string]string{"__content": "old", "tag": "removed"},
		Children:   []db.HashSetItem{{Key: base + ":chunk:old", Fields: map[string]string{"__content": "old span"}}},
	}
	r, err := s.MutateHash(ctx, m)
	if err != nil || !r.Created || r.Revision != 1 {
		t.Fatalf("create %+v %v", r, err)
	}
	verifyFailedCommit(t, s, addr, base, key, stage)
	verifyConcurrentWrites(t, s, ctx, key, stage, m)
	verifyPatchAndDelete(t, s, ctx, key, stage)
}

func verifyConcurrentWrites(t *testing.T, s *Store, ctx context.Context, key, stage string, m *db.HashMutation) {
	t.Helper()
	expected := 1
	m.ExpectedRevision = &expected
	m.Set = map[string]string{"__content": "new"}
	m.Children = []db.HashSetItem{
		{Key: strings.TrimSuffix(key, ":doc:1") + ":chunk:new", Fields: map[string]string{"__content": "new span"}},
	}
	var succeeded, conflicted atomic.Int32
	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, writeErr := s.MutateHash(ctx, m)
			switch {
			case writeErr == nil:
				succeeded.Add(1)
			case errors.Is(writeErr, domain.ErrRevisionConflict):
				conflicted.Add(1)
			default:
				t.Errorf("concurrent write: %v", writeErr)
			}
		}()
	}
	wg.Wait()
	if succeeded.Load() != 1 || conflicted.Load() != 15 {
		t.Fatalf("success %d conflicts %d", succeeded.Load(), conflicted.Load())
	}
	fields, err := s.HGetAll(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	if fields["__revision"] != "2" || fields["__content"] != "new" || fields["tag"] != "" {
		t.Fatalf("replace fields %+v", fields)
	}
	if exists, err := s.Exists(ctx, m.Children[0].Key); err != nil || !exists {
		t.Fatalf("winning fragment missing: %t %v", exists, err)
	}
	_, err = s.MutateHash(ctx, &db.HashMutation{Key: key, StagingKey: stage, Delete: true, ExpectedRevision: &expected})
	if !errors.Is(err, domain.ErrRevisionConflict) {
		t.Fatalf("stale delete: %v", err)
	}

}

func verifyPatchAndDelete(t *testing.T, s *Store, ctx context.Context, key, stage string) {
	t.Helper()
	expected := 2

	r, err := s.MutateHash(
		ctx,
		&db.HashMutation{
			Key:              key,
			StagingKey:       stage,
			ExpectedRevision: &expected,
			RequireExists:    true,
			Set:              map[string]string{"tag": "patched"},
			Remove:           []string{"__content"},
		},
	)
	if err != nil || r.Revision != 3 {
		t.Fatalf("patch %+v %v", r, err)
	}
	fields, err := s.HGetAll(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	if fields["tag"] != "patched" || fields["__content"] != "" {
		t.Fatalf("patch fields %+v", fields)
	}
	if exists, err := s.Exists(ctx, stage); err != nil || exists {
		t.Fatalf("staging survived: %t %v", exists, err)
	}
	_, err = s.MutateHash(ctx, &db.HashMutation{Key: key, StagingKey: stage, Delete: true, RequireExists: true})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.MutateHash(
		ctx,
		&db.HashMutation{Key: key, StagingKey: stage, RequireExists: true, Set: map[string]string{"tag": "resurrect"}},
	)
	if !errors.Is(err, domain.ErrDocumentNotFound) {
		t.Fatalf("patch resurrected deleted key: %v", err)
	}
}

func verifyFailedCommit(t *testing.T, admin *Store, addr, base, key, stage string) {
	t.Helper()
	ctx := context.Background()
	user := fmt.Sprintf("vecdex-fault-%d", time.Now().UnixNano())
	cmd := admin.b().
		Arbitrary("ACL").
		Args("SETUSER", user, "reset", "on", ">fault-injection-password", "~"+base+"*",
			"+@all", "-rename", "-flushdb", "-flushall", "-acl").
		Build()
	if err := admin.do(ctx, cmd).Error(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = admin.do(ctx, admin.b().Arbitrary("ACL").Args("DELUSER", user).Build()).Error() }()
	restricted, err := NewStore(
		Config{Addrs: []string{addr}, Standalone: true, Username: user, Password: "fault-injection-password"},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer restricted.Close()
	_, err = restricted.MutateHash(
		ctx,
		&db.HashMutation{
			Key:        key,
			StagingKey: stage,
			Replace:    true,
			Set:        map[string]string{"__content": "uncommitted"},
			Children: []db.HashSetItem{
				{Key: base + ":chunk:failed", Fields: map[string]string{"__content": "failed span"}},
			},
		},
	)
	if err == nil {
		t.Fatal("expected denied RENAME during commit")
	}
	fields, err := admin.HGetAll(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	if fields["__content"] != "old" || fields["__revision"] != "1" {
		t.Fatalf("failed commit damaged original: %+v", fields)
	}
	if exists, err := admin.Exists(ctx, base+":chunk:old"); err != nil || !exists {
		t.Fatalf("old fragment lost: %t %v", exists, err)
	}
	if exists, err := admin.Exists(ctx, base+":chunk:failed"); err != nil || exists {
		t.Fatalf("failed fragment survived: %t %v", exists, err)
	}

}
