package memorycontext

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/redis/rueidis"
)

func liveService(t *testing.T) (*Service, rueidis.Client, string) {
	t.Helper()
	addr := os.Getenv("VECDEX_TEST_VALKEY_ADDR")
	if addr == "" {
		t.Skip("VECDEX_TEST_VALKEY_ADDR is not set")
	}
	client, err := rueidis.NewClient(rueidis.ClientOption{
		InitAddress: []string{addr}, ForceSingleClient: true, DisableCache: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	owner := "context-test-" + opaqueID()
	t.Cleanup(func() { cleanupOwner(t, client, owner); client.Close() })
	return New(client), client, owner
}

func cleanupOwner(t *testing.T, client rueidis.Client, owner string) {
	t.Helper()
	ctx := context.Background()
	cursor := "0"
	for {
		command := client.B().Arbitrary("SCAN").Args(cursor, "MATCH", ownerBase(owner)+"*", "COUNT", "100").Build()
		values, err := client.Do(ctx, command).ToArray()
		if err != nil {
			t.Error(err)
			return
		}
		cursor, err = values[0].ToString()
		if err != nil {
			t.Error(err)
			return
		}
		keys, err := values[1].AsStrSlice()
		if err != nil {
			t.Error(err)
			return
		}
		if len(keys) > 0 {
			if err := client.Do(ctx, client.B().Del().Key(keys...).Build()).Error(); err != nil {
				t.Error(err)
				return
			}
		}
		if cursor == "0" {
			return
		}
	}
}

type lifecycleFixture struct {
	request       CreateRequest
	first, second MutationResult
	update        UpdateRequest
}

func TestContextLifecycleLive(t *testing.T) {
	service, client, owner := liveService(t)
	fixture := createAndUpdateContext(t, service, client, owner)
	assertCreateReplay(t, service, &fixture)
	if _, err := service.Get(context.Background(), owner+"other", fixture.first.Context.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("owner isolation: %v", err)
	}
	assertCloseReplay(t, service, client, &fixture)
}

func createAndUpdateContext(t *testing.T, service *Service, client rueidis.Client, owner string) lifecycleFixture {
	t.Helper()
	ctx := context.Background()
	request := CreateRequest{Owner: owner, Name: "work", Collection: "notes", Generation: "generation",
		IdempotencyKey: "create",
		Pins:           []Pin{{DocumentID: "source", Version: "source-version"}}}
	first, err := service.Create(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if first.Replayed || first.Context.ExpiresAt == nil || first.CurrentState != "present" {
		t.Fatalf("create: %+v", first)
	}
	if duration := first.Context.ExpiresAt.Sub(first.Context.CreatedAt); duration != DefaultLifetime {
		t.Fatalf("expiry: %s", duration)
	}
	before, _ := client.Do(ctx, client.B().Pttl().Key(recordKey(owner, first.Context.ID)).Build()).AsInt64()
	if _, err := service.Get(ctx, owner, first.Context.ID); err != nil {
		t.Fatal(err)
	}
	after, _ := client.Do(ctx, client.B().Pttl().Key(recordKey(owner, first.Context.ID)).Build()).AsInt64()
	if after > before || after <= 0 {
		t.Fatalf("read extended expiry: %d -> %d", before, after)
	}
	name := "changed"
	update := UpdateRequest{Owner: owner, ID: first.Context.ID, ExpectedVersion: first.Context.Version,
		IdempotencyKey: "update", Name: &name}
	second, err := service.Update(ctx, update)
	if err != nil || second.Context.Version == first.Context.Version {
		t.Fatalf("update: %+v %v", second, err)
	}
	stale := update
	stale.IdempotencyKey = "stale"
	if _, err := service.Update(ctx, stale); !errors.Is(err, ErrConflict) {
		t.Fatalf("CAS: %v", err)
	}
	return lifecycleFixture{request: request, first: first, second: second, update: update}
}

func assertCreateReplay(t *testing.T, service *Service, fixture *lifecycleFixture) {
	t.Helper()
	ctx := context.Background()
	request, first, second := fixture.request, fixture.first, fixture.second
	replay, err := service.Create(ctx, request)
	if err != nil || !replay.Replayed || replay.Context.Version != first.Context.Version ||
		replay.CurrentVersion != second.Context.Version {
		t.Fatalf("create replay after update: %+v %v", replay, err)
	}
	changed := request
	changed.Name = "different"
	if _, err := service.Create(ctx, changed); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("fingerprint conflict: %v", err)
	}
}

func assertCloseReplay(t *testing.T, service *Service, client rueidis.Client, fixture *lifecycleFixture) {
	t.Helper()
	ctx := context.Background()
	request, first, second, update := fixture.request, fixture.first, fixture.second, fixture.update
	owner := request.Owner
	closed, err := service.Delete(ctx, DeleteRequest{Owner: owner, ID: second.Context.ID,
		ExpectedVersion: second.Context.Version, IdempotencyKey: "close"})
	if err != nil || closed.CurrentState != "absent" {
		t.Fatalf("delete: %+v %v", closed, err)
	}
	replay, err := service.Create(ctx, request)
	assertAbsentReplay(t, &replay, err)
	if _, err := service.Get(ctx, owner, first.Context.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("resurrected context: %v", err)
	}
	replay, err = service.Update(ctx, update)
	assertAbsentReplay(t, &replay, err)
	replay, err = service.Delete(ctx, DeleteRequest{Owner: owner, ID: second.Context.ID,
		ExpectedVersion: second.Context.Version, IdempotencyKey: "close"})
	assertAbsentReplay(t, &replay, err)
	receiptTTL, err := client.Do(ctx, client.B().Pttl().Key(receiptKey(owner, "create")).Build()).AsInt64()
	if err != nil || receiptTTL <= 0 || receiptTTL > ReceiptLifetime.Milliseconds() {
		t.Fatalf("receipt lifetime %d %v", receiptTTL, err)
	}
	page, err := service.List(ctx, ListRequest{Owner: owner})
	if err != nil || len(page.Contexts) != 0 {
		t.Fatalf("deleted catalog entry: %+v %v", page, err)
	}
}

func assertAbsentReplay(t *testing.T, result *MutationResult, err error) {
	t.Helper()
	if err != nil || !result.Replayed || result.CurrentState != "absent" {
		t.Fatalf("absent replay: %+v %v", result, err)
	}
}

func TestContextConcurrentMutationLive(t *testing.T) {
	service, _, owner := liveService(t)
	ctx := context.Background()
	request := CreateRequest{Owner: owner, Name: "work", Collection: "notes", Generation: "g", IdempotencyKey: "same"}
	ids := make(chan string, 16)
	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := service.Create(ctx, request)
			if err != nil {
				t.Error(err)
				return
			}
			ids <- result.Context.ID
		}()
	}
	wg.Wait()
	close(ids)
	id := ""
	for next := range ids {
		if id != "" && id != next {
			t.Fatal("duplicate contexts for one request")
		}
		id = next
	}
	current, err := service.Get(ctx, owner, id)
	if err != nil {
		t.Fatal(err)
	}
	var writes, conflicts atomic.Int32
	for i := range 16 {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			name := fmt.Sprintf("name-%d", index)
			_, err := service.Update(ctx, UpdateRequest{Owner: owner, ID: id, ExpectedVersion: current.Version,
				IdempotencyKey: name, Name: &name})
			switch {
			case err == nil:
				writes.Add(1)
			case errors.Is(err, ErrConflict):
				conflicts.Add(1)
			default:
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	if writes.Load() != 1 || conflicts.Load() != 15 {
		t.Fatalf("writes=%d conflicts=%d", writes.Load(), conflicts.Load())
	}
}

func TestContextExpiryLive(t *testing.T) {
	service, client, owner := liveService(t)
	ctx := context.Background()
	request := CreateRequest{Owner: owner, Name: "expires", Collection: "notes", Generation: "g", IdempotencyKey: "expiry"}
	at := time.Now().Add(350 * time.Millisecond)
	request.Expiry = Expiry{Set: true, At: &at}
	created, err := service.Create(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		_, err = service.Get(ctx, owner, created.Context.ID)
		if errors.Is(err, ErrNotFound) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !errors.Is(err, ErrNotFound) {
		t.Fatal("native expiry did not fire")
	}
	replay, err := service.Create(ctx, request)
	if err != nil || !replay.Replayed || replay.CurrentState != "absent" {
		t.Fatalf("expired replay: %+v %v", replay, err)
	}
	page, err := service.List(ctx, ListRequest{Owner: owner})
	if err != nil || len(page.Contexts) != 0 {
		t.Fatalf("expiry cleanup: %+v %v", page, err)
	}
	count, _ := client.Do(ctx, client.B().Zcard().Key(expiryKey(owner)).Build()).AsInt64()
	if count != 0 {
		t.Fatalf("expiry index leaked %d entries", count)
	}
}

func TestContextPersistenceAndPaginationLive(t *testing.T) {
	service, client, owner := liveService(t)
	ctx := context.Background()
	request := CreateRequest{Owner: owner, Name: "persistent", Collection: "notes", Generation: "g"}
	for i := range 5 {
		request.IdempotencyKey = fmt.Sprintf("persistent-%d", i)
		request.Expiry = Expiry{Set: true}
		result, err := service.Create(ctx, request)
		if err != nil {
			t.Fatal(err)
		}
		ttl, _ := client.Do(ctx, client.B().Pttl().Key(recordKey(owner, result.Context.ID)).Build()).AsInt64()
		if result.Context.ExpiresAt != nil || ttl != -1 {
			t.Fatalf("persistent ttl %d", ttl)
		}
	}
	seen := map[string]bool{}
	cursor := ""
	for range 4 {
		page, err := service.List(ctx, ListRequest{Owner: owner, Cursor: cursor, Limit: 2})
		if err != nil || len(page.Contexts) > 2 {
			t.Fatalf("page: %+v %v", page, err)
		}
		for _, value := range page.Contexts {
			if seen[value.ID] {
				t.Fatal("duplicate page item")
			}
			seen[value.ID] = true
		}
		cursor = page.NextCursor
		if cursor == "" {
			break
		}
	}
	if len(seen) != 5 || cursor != "" {
		t.Fatalf("pagination returned %d", len(seen))
	}
}

func TestContextNativeWithoutScriptingLive(t *testing.T) {
	_, admin, owner := liveService(t)
	ctx := context.Background()
	user := "context-native-" + opaqueID()
	command := admin.B().Arbitrary("ACL").Args("SETUSER", user, "reset", "on", ">context-test-password",
		"~"+ownerBase(owner)+"*", "+@all", "-eval", "-evalsha", "-eval_ro", "-evalsha_ro", "-script",
		"-fcall", "-fcall_ro", "-function", "-flushdb", "-flushall", "-acl").Build()
	if err := admin.Do(ctx, command).Error(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Do(ctx, admin.B().Arbitrary("ACL").Args("DELUSER", user).Build()).Error() })
	client, err := rueidis.NewClient(rueidis.ClientOption{InitAddress: []string{os.Getenv("VECDEX_TEST_VALKEY_ADDR")},
		ForceSingleClient: true, DisableCache: true, Username: user, Password: "context-test-password"})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	service := New(client)
	fixture := createAndUpdateContext(t, service, client, owner)
	assertCreateReplay(t, service, &fixture)
	assertCloseReplay(t, service, client, &fixture)
}
