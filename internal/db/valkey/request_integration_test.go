package valkey

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/kailas-cloud/vecdex/internal/db"
	"github.com/kailas-cloud/vecdex/internal/domain"
)

func TestWriteRequestReceiptSurvivesRestartLive(t *testing.T) {
	store, cfg, layout, mutation := workFixture(t)
	ctx := context.Background()
	request := &db.WriteRequest{Key: layout.Request("source", "external-key"), Owner: "owner-one",
		Fingerprint: "input-one", GuardKey: mutation.GuardKey, GuardGeneration: mutation.GuardGeneration}
	if result, err := store.ClaimWriteRequest(ctx, request); err != nil || result.Replay {
		t.Fatalf("claim=%+v %v", result, err)
	}
	mutation.Request = request
	written, err := store.MutateHash(ctx, mutation)
	if err != nil || written.Version == "" {
		t.Fatalf("commit=%+v %v", written, err)
	}
	store.Close()
	restarted, err := NewStore(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	request.Owner = "owner-after-restart"
	replay, err := restarted.ClaimWriteRequest(ctx, request)
	if err != nil || !replay.Replay || replay.Result != written {
		t.Fatalf("restart lost original outcome: %+v %v", replay, err)
	}
	ttl, err := restarted.client.Do(ctx, restarted.b().Pttl().Key(request.Key).Build()).AsInt64()
	if err != nil || ttl < (24*time.Hour-time.Minute).Milliseconds() || ttl > (24*time.Hour).Milliseconds() {
		t.Fatalf("receipt is not retained for 24 hours: ttl=%d err=%v", ttl, err)
	}
	request.Fingerprint = "changed-input"
	if _, err := restarted.ClaimWriteRequest(ctx, request); !errors.Is(err, domain.ErrIdempotencyConflict) {
		t.Fatalf("different input replaced a committed receipt: %v", err)
	}
}

func TestWriteRequestReleaseFencesAnOldOwnerLive(t *testing.T) {
	store, _, layout, mutation := workFixture(t)
	ctx := context.Background()
	first := &db.WriteRequest{Key: layout.Request("source", "external-key"), Owner: "first",
		Fingerprint: "same-input", GuardKey: mutation.GuardKey, GuardGeneration: mutation.GuardGeneration}
	if _, err := store.ClaimWriteRequest(ctx, first); err != nil {
		t.Fatal(err)
	}
	second := *first
	second.Owner = "second"
	if _, err := store.ClaimWriteRequest(ctx, &second); !errors.Is(err, domain.ErrIdempotencyInProgress) {
		t.Fatalf("active owner was not protected: %v", err)
	}
	if err := store.ReleaseWriteRequest(ctx, first); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ClaimWriteRequest(ctx, &second); err != nil {
		t.Fatal(err)
	}
	mutation.Request = first
	if _, err := store.MutateHash(ctx, mutation); !errors.Is(err, domain.ErrIdempotencyInProgress) {
		t.Fatalf("released owner committed over its successor: %v", err)
	}
	if parent, err := store.HGetAll(ctx, mutation.Key); err != nil || len(parent) != 0 {
		t.Fatalf("fenced owner published a source: %v %v", parent, err)
	}
	mutation.Request = &second
	if _, err := store.MutateHash(ctx, mutation); err != nil {
		t.Fatal(err)
	}
	if err := store.ReleaseWriteRequest(ctx, &second); err != nil {
		t.Fatal(err)
	}
	if receipt, err := store.HGetAll(ctx, second.Key); err != nil || receipt["state"] != "complete" {
		t.Fatalf("release removed a committed receipt: %v %v", receipt, err)
	}
}

func TestExpiredWriteRequestFencesAnOldOwnerLive(t *testing.T) {
	store, _, layout, mutation := workFixture(t)
	ctx := context.Background()
	first := &db.WriteRequest{Key: layout.Request("source", "external-key"), Owner: "first",
		Fingerprint: "same-input", GuardKey: mutation.GuardKey, GuardGeneration: mutation.GuardGeneration}
	if _, err := store.ClaimWriteRequest(ctx, first); err != nil {
		t.Fatal(err)
	}
	// Expire the abandoned reservation through the public command, without a
	// ten-minute test delay. The parent and durable work have not been published.
	if err := store.client.Do(ctx, store.b().Pexpire().Key(first.Key).Milliseconds(1).Build()).Error(); err != nil {
		t.Fatal(err)
	}
	waitForRequestExpiry(t, store, first.Key)
	second := *first
	second.Owner = "successor"
	if result, err := store.ClaimWriteRequest(ctx, &second); err != nil || result.Replay {
		t.Fatalf("expired reservation could not be reacquired: %+v %v", result, err)
	}
	mutation.Request = first
	if _, err := store.MutateHash(ctx, mutation); !errors.Is(err, domain.ErrIdempotencyInProgress) {
		t.Fatalf("expired owner was not fenced: %v", err)
	}
	if exists, err := store.Exists(ctx, mutation.Key); err != nil || exists {
		t.Fatalf("expired owner published a source: %v %v", exists, err)
	}
	mutation.Request = &second
	committed, err := store.MutateHash(ctx, mutation)
	if err != nil || committed.Version == "" {
		t.Fatalf("successor could not commit: %+v %v", committed, err)
	}
	replay, err := store.ClaimWriteRequest(ctx, first)
	if err != nil || !replay.Replay || replay.Result != committed {
		t.Fatalf("old caller did not receive successor's outcome: %+v %v", replay, err)
	}
}

func waitForRequestExpiry(t *testing.T, store *Store, key string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		exists, err := store.Exists(context.Background(), key)
		if err != nil {
			t.Fatal(err)
		}
		if !exists {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("abandoned reservation did not expire")
		}
		time.Sleep(time.Millisecond)
	}
}
