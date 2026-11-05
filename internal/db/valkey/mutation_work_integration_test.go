package valkey

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/kailas-cloud/vecdex/internal/db"
	"github.com/kailas-cloud/vecdex/internal/domain"
	"github.com/kailas-cloud/vecdex/internal/domain/storage"
)

func workFixture(t *testing.T) (*Store, Config, storage.Collection, *db.HashMutation) {
	t.Helper()
	addr := os.Getenv("VECDEX_TEST_VALKEY_ADDR")
	if addr == "" {
		t.Skip("isolated Valkey address is required")
	}
	cfg := Config{Addrs: []string{addr}, Standalone: true}
	store, err := NewStore(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	layout, err := storage.NewCollection("work-test", rand.Text())
	if err != nil {
		t.Fatal(err)
	}
	id := "source"
	p := storage.Partition(id)
	if err := store.HSet(context.Background(), layout.Guard(p), map[string]string{
		"generation": "test", "state": "active",
	}); err != nil {
		t.Fatal(err)
	}
	m := &db.HashMutation{
		Key: layout.Document(id), StagingKey: layout.Staging(id, "prepare"),
		WorkQueueKey: layout.WorkQueue(p), CatalogKey: layout.Catalog(p), CatalogMember: id,
		GuardKey: layout.Guard(p), GuardGeneration: "test", GuardState: "active", Replace: true,
		Set:      map[string]string{"__content": "whole source"},
		Children: []db.HashSetItem{{Key: layout.Fragment(id, "one", 0), Fields: map[string]string{"text": "fragment"}}},
	}
	return store, cfg, layout, m
}

func expirePreparedWork(t *testing.T, store *Store, queue string, work *mutationWork) {
	t.Helper()
	work.Deadline = time.Now().Add(-time.Minute).UnixMilli()
	client, release := store.client.Dedicate()
	defer release()
	command := mutationWorkCommand(client, queue, work)
	if err := client.Do(context.Background(), command).Error(); err != nil {
		t.Fatal(err)
	}
	if err := store.do(context.Background(), store.b().Zadd().Key(queue).
		ScoreMember().ScoreMember(0, work.ID).Build()).Error(); err != nil {
		t.Fatal(err)
	}
}

func TestPreparedWorkSurvivesRestartAndFencesLateWriterLive(t *testing.T) {
	store, cfg, _, m := workFixture(t)
	ctx := context.Background()
	work, fields, err := newMutationWork(m, nil, nil, db.HashMutationResult{Created: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.registerMutationWork(ctx, m.WorkQueueKey, &work); err != nil {
		t.Fatal(err)
	}
	if err := store.prepareWorkBatch(ctx, m, &work, m.Children); err != nil {
		t.Fatal(err)
	}
	expirePreparedWork(t, store, m.WorkQueueKey, &work)
	store.Close()
	recovered, err := NewStore(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer recovered.Close()
	count, err := recovered.RecoverMutations(ctx, m.WorkQueueKey, 10)
	if err != nil || count != 1 {
		t.Fatalf("recovery count=%d err=%v", count, err)
	}
	if exists, err := recovered.Exists(ctx, m.Children[0].Key); err != nil || exists {
		t.Fatalf("abandoned fragment survived: exists=%v err=%v", exists, err)
	}
	err = recovered.prepareWorkBatch(ctx, m, &work, []db.HashSetItem{{Key: work.Staging, Fields: fields}})
	if !errors.Is(err, errMutationFenced) {
		t.Fatalf("late writer was not fenced: %v", err)
	}
	if exists, _ := recovered.Exists(ctx, work.Staging); exists {
		t.Fatal("late writer recreated prepared payload")
	}
}

func TestMutationReceiptOutlivesLaterWritesAndDeleteLive(t *testing.T) {
	store, _, layout, m := workFixture(t)
	ctx := context.Background()
	first, err := store.MutateHash(ctx, m)
	if err != nil || first.Revision != 1 || first.OperationID == "" {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	old := m.Children[0].Key
	m.Children[0].Key = layout.Fragment("source", "two", 0)
	second, err := store.MutateHash(ctx, m)
	if err != nil || second.Revision != 2 {
		t.Fatalf("second=%+v err=%v", second, err)
	}
	if exists, _ := store.Exists(ctx, old); exists {
		t.Fatal("old committed fragments were not cleaned")
	}
	// Resolving an earlier lost response must not depend on the current parent.
	requireMutationReceipt(t, store, m.WorkQueueKey, first.OperationID, 1)
	current := m.Children[0].Key
	m.Delete, m.RequireExists = true, true
	deleted, err := store.MutateHash(ctx, m)
	if err != nil {
		t.Fatal(err)
	}
	if exists, _ := store.Exists(ctx, current); exists {
		t.Fatal("delete left source fragments")
	}
	requireMutationReceipt(t, store, m.WorkQueueKey, deleted.OperationID, deleted.Revision)
	if count, err := store.CatalogCount(ctx, m.CatalogKey); err != nil || count != 0 {
		t.Fatalf("count=%d err=%v", count, err)
	}
}

func TestAbortedPreparationDoesNotDeleteForeignPayloadLive(t *testing.T) {
	store, _, _, m := workFixture(t)
	ctx := context.Background()
	foreign := m.Children[0].Key
	foreignFields := map[string]string{"__prepared_by": "another-operation", "text": "live"}
	if err := store.HSet(ctx, foreign, foreignFields); err != nil {
		t.Fatal(err)
	}
	work, fields, err := newMutationWork(m, nil, nil, db.HashMutationResult{})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.registerMutationWork(ctx, m.WorkQueueKey, &work); err != nil {
		t.Fatal(err)
	}
	if err := store.prepareMutationWork(ctx, m, &work, fields); !errors.Is(err, errPreparedChildBusy) {
		t.Fatalf("expected owned key conflict: %v", err)
	}
	recovered := store.recoverFailedWork(m.WorkQueueKey, work.ID)
	if recovered == nil || recovered.State != "aborted" {
		t.Fatalf("recovery=%+v", recovered)
	}
	row, err := store.HGetAll(ctx, foreign)
	if err != nil || row["text"] != "live" {
		t.Fatalf("foreign payload deleted: %v err=%v", row, err)
	}
}

func requireMutationReceipt(t *testing.T, store *Store, queue, id string, revision int) {
	t.Helper()
	receipt := store.recoverFailedWork(queue, id)
	if receipt == nil || !receipt.Committed || receipt.Result.Revision != revision {
		t.Fatalf("missing committed receipt %s: %+v", id, receipt)
	}
}

func TestCleanupFailureRemainsQueuedAndResumesLive(t *testing.T) {
	store, cfg, layout, m := workFixture(t)
	ctx := context.Background()
	m.Children = make([]db.HashSetItem, 100)
	for i := range m.Children {
		m.Children[i] = db.HashSetItem{
			Key:    layout.Fragment("source", "old", i),
			Fields: map[string]string{"text": "old"},
		}
	}
	if _, err := store.MutateHash(ctx, m); err != nil {
		t.Fatal(err)
	}
	restricted := workStoreWithoutUnlink(t, store, cfg, layout)
	m.Children = []db.HashSetItem{{Key: layout.Fragment("source", "new", 0), Fields: map[string]string{"text": "new"}}}
	result, err := restricted.MutateHash(ctx, m)
	if err != nil {
		t.Fatal(err)
	}
	if exists, _ := store.Exists(ctx, layout.Fragment("source", "old", 0)); !exists {
		t.Fatal("test did not leave failed cleanup work")
	}
	// Persist one acknowledged cleanup batch, then replace the worker process.
	client, release := store.client.Dedicate()
	pending, err := readMutationWork(ctx, client, m.WorkQueueKey, result.OperationID)
	release()
	if err != nil {
		t.Fatal(err)
	}
	work, err := store.cleanupWorkBatch(ctx, m.WorkQueueKey, &pending)
	if err != nil || work.Progress != workBatchSize {
		t.Fatalf("work=%+v err=%v", work, err)
	}
	if exists, _ := store.Exists(ctx, layout.Fragment("source", "old", 64)); !exists {
		t.Fatal("cleanup was not interrupted between batches")
	}
	restarted, err := NewStore(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	count, err := restarted.RecoverMutations(ctx, m.WorkQueueKey, 10)
	if err != nil || count != 1 {
		t.Fatalf("resumed recovery count=%d err=%v", count, err)
	}
	verifyCleanedVersion(t, restarted, layout, m)
	requireMutationReceipt(t, restarted, m.WorkQueueKey, result.OperationID, 2)
}

func workStoreWithoutUnlink(t *testing.T, admin *Store, cfg Config, layout storage.Collection) *Store {
	t.Helper()
	user := "work-" + rand.Text()
	command := admin.b().Arbitrary("ACL").Args("SETUSER", user, "reset", "on", ">test-password",
		"~"+layout.Base()+"*", "+@all", "-unlink", "-eval", "-evalsha", "-script").Build()
	if err := admin.do(context.Background(), command).Error(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = admin.do(context.Background(), admin.b().Arbitrary("ACL").Args("DELUSER", user).Build()).Error()
	})
	cfg.Username, cfg.Password = user, "test-password"
	store, err := NewStore(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	return store
}

func verifyCleanedVersion(t *testing.T, store *Store, layout storage.Collection, m *db.HashMutation) {
	t.Helper()
	ctx := context.Background()
	for i := range 100 {
		if exists, err := store.Exists(ctx, layout.Fragment("source", "old", i)); exists || err != nil {
			t.Fatalf("old fragment %d exists=%v err=%v", i, exists, err)
		}
	}
	if exists, err := store.Exists(ctx, m.Children[0].Key); err != nil || !exists {
		t.Fatalf("current fragment was lost: exists=%v err=%v", exists, err)
	}
	if count, err := store.CatalogCount(ctx, m.WorkQueueKey); err != nil || count != 0 {
		t.Fatalf("work queue count=%d err=%v", count, err)
	}
}

func TestRecoveryFencesAlreadyWatchedCommitLive(t *testing.T) {
	store, _, _, m := workFixture(t)
	ctx := context.Background()
	work, fields, err := newMutationWork(m, nil, nil, db.HashMutationResult{Created: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.registerMutationWork(ctx, m.WorkQueueKey, &work); err != nil {
		t.Fatal(err)
	}
	if err := store.prepareMutationWork(ctx, m, &work, fields); err != nil {
		t.Fatal(err)
	}
	client, release := store.client.Dedicate()
	defer release()
	defer unwatch(client)
	if err := watchKeys(ctx, client, m.Key, m.GuardKey, workKey(m.WorkQueueKey, work.ID), work.Staging); err != nil {
		t.Fatal(err)
	}
	// Pause between the final eligibility read and EXEC. Another worker fences
	// this exact journal and removes its prepared keys in the meantime.
	eligible, err := readMutationWork(ctx, client, m.WorkQueueKey, work.ID)
	if err != nil || eligible.State != "preparing" {
		t.Fatalf("eligible=%+v err=%v", eligible, err)
	}
	recovered := store.recoverFailedWork(m.WorkQueueKey, work.ID)
	if recovered == nil || recovered.State != "aborted" {
		t.Fatal("recovery did not fence the preparation")
	}
	work.State, work.Committed = "committed", true
	commands := mutationCommitCommands(client, m, work.Staging, work.Result.Version)
	commands = append(commands, mutationWorkCommand(client, m.WorkQueueKey, &work))
	_, committed, err := execTransaction(ctx, client, commands)
	if err != nil || committed {
		t.Fatalf("fenced commit: committed=%v err=%v", committed, err)
	}
	if exists, err := store.Exists(ctx, m.Key); exists || err != nil {
		t.Fatalf("late parent exists=%v err=%v", exists, err)
	}
}

func TestRegistrationCannotRecreateQueueAfterGuardPurgeLive(t *testing.T) {
	for _, state := range []string{"active", "deleting"} {
		t.Run(state, func(t *testing.T) {
			store, _, _, m := workFixture(t)
			ctx := context.Background()
			m.GuardState = state
			if err := store.HSet(ctx, m.GuardKey, map[string]string{"state": state}); err != nil {
				t.Fatal(err)
			}
			work, _, err := newMutationWork(m, nil, nil, db.HashMutationResult{})
			if err != nil {
				t.Fatal(err)
			}
			encoded, err := json.Marshal(work)
			if err != nil {
				t.Fatal(err)
			}
			if err := store.SetWithTTL(ctx, workKey(m.WorkQueueKey, work.ID), encoded, time.Minute); err != nil {
				t.Fatal(err)
			}
			if err := store.HSet(ctx, m.GuardKey, map[string]string{"state": "purging"}); err != nil {
				t.Fatal(err)
			}
			err = store.registerMutationMembership(ctx, m.WorkQueueKey, &work, string(encoded))
			if !errors.Is(err, domain.ErrNotFound) {
				t.Fatalf("late queue registration: %v", err)
			}
			if count, err := store.CatalogCount(ctx, m.WorkQueueKey); err != nil || count != 0 {
				t.Fatalf("recreated queue=%d err=%v", count, err)
			}
		})
	}
}

func TestPersistentJournalCannotReturnAfterFinalCancellationLive(t *testing.T) {
	store, _, _, m := workFixture(t)
	ctx := context.Background()
	work, _, err := newMutationWork(m, nil, nil, db.HashMutationResult{})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(work)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetWithTTL(ctx, workKey(m.WorkQueueKey, work.ID), encoded, time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := store.registerMutationMembership(ctx, m.WorkQueueKey, &work, string(encoded)); err != nil {
		t.Fatal(err)
	}
	if err := store.HSet(ctx, m.GuardKey, map[string]string{"state": "purging"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CancelMutations(ctx, m.WorkQueueKey, 64); err != nil {
		t.Fatal(err)
	}
	err = store.publishMutationIntent(ctx, m.WorkQueueKey, &work, string(encoded))
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("late journal publication: %v", err)
	}
	if exists, err := store.Exists(ctx, workKey(m.WorkQueueKey, work.ID)); err != nil || exists {
		t.Fatalf("journal recreated=%v err=%v", exists, err)
	}
	if count, err := store.CatalogCount(ctx, m.WorkQueueKey); err != nil || count != 0 {
		t.Fatalf("queue recreated=%d err=%v", count, err)
	}
}
