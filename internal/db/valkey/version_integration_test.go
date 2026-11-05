package valkey

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/kailas-cloud/vecdex/internal/db"
	"github.com/kailas-cloud/vecdex/internal/domain"
)

func TestOpaqueVersionFencesDeleteRecreateLive(t *testing.T) {
	store, _, layout, m := workFixture(t)
	ctx := context.Background()
	first, err := store.MutateHash(ctx, m)
	if err != nil || first.Version == "" {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	update := *m
	update.ExpectedVersion = &first.Version
	update.Set = map[string]string{"__content": "second source"}
	update.Children = []db.HashSetItem{
		{Key: layout.Fragment("source", "two", 0), Fields: map[string]string{"text": "second"}},
	}
	second, err := store.MutateHash(ctx, &update)
	if err != nil || second.Version == first.Version || second.Revision != 2 {
		t.Fatalf("second=%+v err=%v", second, err)
	}
	if _, err := store.MutateHash(ctx, &update); !errors.Is(err, domain.ErrRevisionConflict) {
		t.Fatalf("stale replacement: %v", err)
	}
	deletion := update
	deletion.ExpectedVersion = &second.Version
	deletion.Delete = true
	if _, err := store.MutateHash(ctx, &deletion); err != nil {
		t.Fatal(err)
	}
	recreated := *m
	recreated.Set = map[string]string{"__content": "recreated source"}
	recreated.Children = []db.HashSetItem{
		{Key: layout.Fragment("source", "recreated", 0), Fields: map[string]string{"text": "recreated"}},
	}
	next, err := store.MutateHash(ctx, &recreated)
	if err != nil || next.Revision != 1 || next.Version == first.Version || next.Version == second.Version {
		t.Fatalf("recreate=%+v err=%v", next, err)
	}
	assertRecreatedVersionProtected(t, store, m, &update, &deletion, next, first.Version)

}

func TestOpaqueVersionReceiptSurvivesRestartLive(t *testing.T) {
	store, cfg, _, m := workFixture(t)
	ctx := context.Background()
	first, err := store.MutateHash(ctx, m)
	if err != nil {
		t.Fatal(err)
	}
	store.Close()
	restarted, err := NewStore(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	dedicated, release := restarted.client.Dedicate()
	defer release()
	receipt, err := readMutationWork(ctx, dedicated, m.WorkQueueKey, first.OperationID)
	if err != nil || receipt.Result.Version != first.Version || receipt.Result.Version == "" {
		t.Fatalf("version receipt=%+v err=%v", receipt.Result, err)
	}
}

func TestConcurrentOpaqueCASHasOneWinnerLive(t *testing.T) {
	store, _, layout, m := workFixture(t)
	ctx := context.Background()
	initial, err := store.MutateHash(ctx, m)
	if err != nil {
		t.Fatal(err)
	}
	var workers sync.WaitGroup
	var winners atomic.Int64
	failures := make(chan error, 12)
	for i := range 12 {
		workers.Go(func() {
			attempt := *m
			attempt.ExpectedVersion = &initial.Version
			attempt.Set = map[string]string{"__content": fmt.Sprintf("winner %d", i)}
			attempt.Children = []db.HashSetItem{
				{
					Key:    layout.Fragment("source", fmt.Sprintf("attempt-%d", i), 0),
					Fields: map[string]string{"text": "candidate"},
				},
			}
			result, err := store.MutateHash(ctx, &attempt)
			if err == nil {
				if result.Version == initial.Version {
					failures <- fmt.Errorf("winner retained stale version")
					return
				}
				winners.Add(1)
			} else if !errors.Is(err, domain.ErrRevisionConflict) {
				failures <- err
			}
		})
	}
	workers.Wait()
	close(failures)
	for err := range failures {
		t.Error(err)
	}
	if winners.Load() != 1 {
		t.Fatalf("CAS winners=%d", winners.Load())
	}
}

func assertRecreatedVersionProtected(
	t *testing.T, store *Store, m, update, deletion *db.HashMutation, next db.HashMutationResult, oldVersion string,
) {
	t.Helper()
	ctx := context.Background()
	first := db.HashMutationResult{Version: oldVersion}
	deletion.ExpectedVersion = &first.Version
	if _, err := store.MutateHash(ctx, deletion); !errors.Is(err, domain.ErrRevisionConflict) {
		t.Fatalf("stale delete crossed recreation: %v", err)
	}
	if _, err := store.MutateHash(ctx, update); !errors.Is(err, domain.ErrRevisionConflict) {
		t.Fatalf("stale write crossed recreation: %v", err)
	}
	parent, err := store.HGetAll(ctx, m.Key)
	if err != nil || parent["__source_version"] != next.Version || parent["__content"] != "recreated source" {
		t.Fatalf("recreated source changed: %v", err)
	}
}
