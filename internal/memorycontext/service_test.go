package memorycontext

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/redis/rueidis"
	"github.com/redis/rueidis/mock"
	"go.uber.org/mock/gomock"
)

func TestValidationBeforeStorage(t *testing.T) {
	service := New(nil)
	valid := CreateRequest{Owner: "owner", Name: "research", Collection: "notes", Generation: "generation",
		IdempotencyKey: "request"}
	cases := []struct {
		name   string
		change func(*CreateRequest)
	}{
		{"owner", func(r *CreateRequest) { r.Owner = "" }},
		{"idempotency", func(r *CreateRequest) { r.IdempotencyKey = "" }},
		{"collection", func(r *CreateRequest) { r.Collection = "" }},
		{"generation", func(r *CreateRequest) { r.Generation = "" }},
		{"pins", func(r *CreateRequest) { r.Pins = make([]Pin, 101) }},
		{"duplicates", func(r *CreateRequest) { r.Pins = []Pin{{"doc", "v1"}, {"doc", "v2"}} }},
		{"pin version", func(r *CreateRequest) { r.Pins = []Pin{{"doc", ""}} }},
		{"search array", func(r *CreateRequest) { r.Search = json.RawMessage(`[]`) }},
		{"search null", func(r *CreateRequest) { r.Search = json.RawMessage(`null`) }},
		{"search invalid", func(r *CreateRequest) { r.Search = json.RawMessage(`{} garbage`) }},
		{"search huge", func(r *CreateRequest) {
			r.Search = json.RawMessage(`{"x":"` + strings.Repeat("a", MaxPayloadBytes) + `"}`)
		}},
		{"expiry", func(r *CreateRequest) { at := time.Now(); r.Expiry.At = &at }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			request := valid
			tc.change(&request)
			if _, err := service.Create(context.Background(), request); !errors.Is(err, ErrInvalid) {
				t.Fatalf("expected validation error, got %v", err)
			}
		})
	}
	_, err := service.Update(context.Background(), UpdateRequest{Owner: "owner", ID: "id", IdempotencyKey: "key"})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("missing CAS accepted: %v", err)
	}
	_, err = service.Delete(context.Background(), DeleteRequest{Owner: "owner", ID: "id", ExpectedVersion: "v"})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("missing idempotency accepted: %v", err)
	}
}

func TestFingerprintCanonicalSettingsAndExplicitExpiry(t *testing.T) {
	one := CreateRequest{Owner: "owner", Name: "name", Collection: "notes", Generation: "g", IdempotencyKey: "key",
		Search: json.RawMessage(`{"filter":{"n":9007199254740993,"a":true},"limit":2}`)}
	two := one
	two.Search = json.RawMessage(`{ "limit":2,"filter": {"a":true,"n":9007199254740993}}`)
	one, err := normalizeCreate(one)
	if err != nil {
		t.Fatal(err)
	}
	two, err = normalizeCreate(two)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := fingerprint("create", one)
	b, _ := fingerprint("create", two)
	if a != b {
		t.Fatal("semantic settings reordered into another request")
	}
	two.Expiry.Set = true
	b, _ = fingerprint("create", two)
	if a == b {
		t.Fatal("persistent and default expiry have same fingerprint")
	}
	two = one
	two.Search = json.RawMessage(`{"filter":{"n":9007199254740992,"a":true},"limit":2}`)
	b, _ = fingerprint("create", two)
	if a == b {
		t.Fatal("numeric precision lost in fingerprint")
	}
}

func TestOwnerKeySlotsAndCursorIsolation(t *testing.T) {
	client := mock.NewClient(gomock.NewController(t))
	owner := "owner{untrusted}"
	keys := []string{recordKey(owner, "record"), catalogKey(owner), expiryKey(owner), receiptKey(owner, "key{other}")}
	command := client.B().Get().Key(keys[0]).Build()
	slot := command.Slot()
	for _, key := range keys[1:] {
		command = client.B().Get().Key(key).Build()
		if command.Slot() != slot {
			t.Fatalf("different hash slot: %s", key)
		}
	}
	if strings.Contains(keys[0], owner) {
		t.Fatal("raw owner entered hash tag")
	}
	cursor := encodeCursor(owner, "last")
	if after, err := decodeCursor(owner, cursor); err != nil || after != "last" {
		t.Fatalf("cursor: %s %v", after, err)
	}
	if _, err := decodeCursor("other", cursor); !errors.Is(err, ErrInvalid) {
		t.Fatalf("foreign cursor: %v", err)
	}
}

func TestReceiptReplayPreservesOriginalAndReportsCurrentState(t *testing.T) {
	now := time.Now().UTC()
	original := Context{ID: "context", Owner: "owner", Version: "original", Generation: "g", Name: "initial"}
	encoded, _ := json.Marshal(receipt{Fingerprint: "digest", Operation: "create", Context: original})
	change := &mutation{owner: "owner", key: "key", digest: "digest"}
	for _, state := range []string{"present", "absent"} {
		t.Run(state, func(t *testing.T) {
			client := mock.NewClient(gomock.NewController(t))
			client.EXPECT().Do(gomock.Any(), mock.Match("GET", receiptKey("owner", "key"))).
				Return(mock.Result(mock.RedisString(string(encoded))))
			result := mock.Result(mock.RedisNil())
			if state == "present" {
				current := original
				current.Version, current.Name = "new", "changed"
				raw, _ := json.Marshal(current)
				result = mock.Result(mock.RedisString(string(raw)))
			}
			client.EXPECT().Do(gomock.Any(), mock.Match("GET", recordKey("owner", "context"))).Return(result)
			replay, found, err := readReceipt(context.Background(), client, change, now)
			if err != nil || !found || !replay.Replayed || replay.Context.Version != "original" || replay.CurrentState != state {
				t.Fatalf("replay %+v found %v error %v", replay, found, err)
			}
			if state == "present" && replay.CurrentVersion != "new" {
				t.Fatal("current version omitted")
			}
		})
	}
}

func TestReceiptChangedPayloadCannotReplay(t *testing.T) {
	client := mock.NewClient(gomock.NewController(t))
	raw, _ := json.Marshal(receipt{Fingerprint: "old", Context: Context{Owner: "owner"}})
	client.EXPECT().Do(gomock.Any(), gomock.Any()).Return(mock.Result(mock.RedisString(string(raw))))
	change := &mutation{owner: "owner", key: "key", digest: "new"}
	_, _, err := readReceipt(context.Background(), client, change, time.Now())
	if !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("changed payload: %v", err)
	}
}

func TestTransactionsRetryOnlyUncommittedWatchConflicts(t *testing.T) {
	cases := []struct {
		name               string
		result             rueidis.RedisResult
		committed, wantErr bool
	}{
		{"watch conflict", mock.Result(mock.RedisNil()), false, false},
		{"network ambiguity", mock.ErrorResult(context.DeadlineExceeded), false, true},
		{"runtime error", mock.Result(mock.RedisArray(mock.RedisError("WRONGTYPE failure"))), false, true},
		{"success", mock.Result(mock.RedisArray(mock.RedisString("OK"))), true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client := mock.NewDedicatedClient(gomock.NewController(t))
			client.EXPECT().DoMulti(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return([]rueidis.RedisResult{
				mock.Result(mock.RedisString("OK")), mock.Result(mock.RedisString("QUEUED")), tc.result})
			committed, err := execute(context.Background(), client, []rueidis.Completed{
				client.B().Set().Key("key").Value("value").Build()})
			if committed != tc.committed || (err != nil) != tc.wantErr {
				t.Fatalf("committed %v error %v", committed, err)
			}
		})
	}
}

func TestExpiredAndForeignRecordsAreUnavailable(t *testing.T) {
	now := time.Now()
	value := Context{ID: "id", Owner: "owner", Version: "v", Generation: "g", ExpiresAt: &now}
	raw, _ := json.Marshal(value)
	if _, err := decodeContext(string(raw), "owner", "id", now); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if _, err := decodeContext(string(raw), "other", "id", now); !errors.Is(err, ErrCorrupt) {
		t.Fatal(err)
	}
}

func TestCreateReplaySkipsBindingValidationAndDoesNotRenewReceipt(t *testing.T) {
	controller := gomock.NewController(t)
	client := mock.NewClient(controller)
	dedicated := mock.NewDedicatedClient(controller)
	client.EXPECT().Dedicate().Return(dedicated, func() {})
	request := CreateRequest{Owner: "owner", Name: "name", Collection: "deleted", Generation: "old-generation",
		IdempotencyKey: "original"}
	request, err := normalizeCreate(request)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := fingerprint("create", request)
	if err != nil {
		t.Fatal(err)
	}
	value := Context{ID: "original-id", Version: "original-version", Owner: request.Owner,
		Collection: request.Collection, Generation: request.Generation}
	raw, err := json.Marshal(receipt{Fingerprint: digest, Operation: "create", Context: value})
	if err != nil {
		t.Fatal(err)
	}
	gomock.InOrder(
		dedicated.EXPECT().Do(gomock.Any(), gomock.Any()).Return(mock.Result(mock.RedisString("OK"))),
		dedicated.EXPECT().Do(gomock.Any(), mock.Match("GET", receiptKey(request.Owner, request.IdempotencyKey))).
			Return(mock.Result(mock.RedisString(string(raw)))),
		dedicated.EXPECT().Do(gomock.Any(), mock.Match("GET", recordKey(request.Owner, value.ID))).
			Return(mock.Result(mock.RedisNil())),
		dedicated.EXPECT().Do(gomock.Any(), mock.Match("UNWATCH")).Return(mock.Result(mock.RedisString("OK"))),
	)
	result, err := New(client).CreateValidated(context.Background(), request, func(context.Context) error {
		t.Fatal("replay consulted deleted collection")
		return errors.New("deleted collection")
	})
	if err != nil || !result.Replayed || result.CurrentState != "absent" || result.Context.ID != value.ID {
		t.Fatalf("receipt replay %+v %v", result, err)
	}
}

func TestFirstCreateRejectsInvalidBindingBeforeWrites(t *testing.T) {
	controller := gomock.NewController(t)
	client := mock.NewClient(controller)
	dedicated := mock.NewDedicatedClient(controller)
	client.EXPECT().Dedicate().Return(dedicated, func() {})
	request := CreateRequest{Owner: "owner", Name: "name", Collection: "deleted", Generation: "g", IdempotencyKey: "key"}
	gomock.InOrder(
		dedicated.EXPECT().Do(gomock.Any(), gomock.Any()).Return(mock.Result(mock.RedisString("OK"))),
		dedicated.EXPECT().Do(gomock.Any(), mock.Match("GET", receiptKey(request.Owner, request.IdempotencyKey))).
			Return(mock.Result(mock.RedisNil())),
		dedicated.EXPECT().Do(gomock.Any(), gomock.Any()).Return(mock.Result(mock.RedisNil())),
		dedicated.EXPECT().Do(gomock.Any(), mock.Match("UNWATCH")).Return(mock.Result(mock.RedisString("OK"))),
	)
	invalidBinding := errors.New("collection generation changed")
	_, err := New(client).CreateValidated(context.Background(), request, func(context.Context) error {
		return invalidBinding
	})
	if !errors.Is(err, invalidBinding) {
		t.Fatalf("binding error lost: %v", err)
	}
}
