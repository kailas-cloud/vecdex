package memorycontext

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/redis/rueidis"
)

type valkeyRepository struct{ client rueidis.Client }

type mutation struct {
	owner, id, key, digest, operation, expected string
	apply                                       func(Context) Context
	validate                                    func(context.Context) error
}

type receipt struct {
	Fingerprint string  `json:"fingerprint"`
	Operation   string  `json:"operation"`
	Context     Context `json:"context"`
}

func ownerBase(owner string) string {
	sum := sha256.Sum256([]byte(owner))
	return "vecdex:mcp:contexts:{" + hex.EncodeToString(sum[:]) + "}"
}

func recordKey(owner, id string) string { return ownerBase(owner) + ":record:" + id }
func catalogKey(owner string) string    { return ownerBase(owner) + ":catalog" }
func expiryKey(owner string) string     { return ownerBase(owner) + ":expiry" }
func receiptKey(owner, key string) string {
	sum := sha256.Sum256([]byte(key))
	return ownerBase(owner) + ":receipt:" + hex.EncodeToString(sum[:])
}

func readContext(ctx context.Context, client rueidis.CoreClient, owner, id string, now time.Time) (Context, error) {
	raw, err := client.Do(ctx, client.B().Get().Key(recordKey(owner, id)).Build()).ToString()
	if rueidis.IsRedisNil(err) {
		return Context{}, ErrNotFound
	}
	if err != nil {
		return Context{}, fmt.Errorf("read memory context: %w", err)
	}
	return decodeContext(raw, owner, id, now)
}

func decodeContext(raw, owner, id string, now time.Time) (Context, error) {
	var value Context
	if len(raw) > MaxPayloadBytes || json.Unmarshal([]byte(raw), &value) != nil {
		return Context{}, ErrCorrupt
	}
	if value.Owner != owner || value.ID != id || value.Version == "" || value.Generation == "" {
		return Context{}, ErrCorrupt
	}
	if value.ExpiresAt != nil && !value.ExpiresAt.After(now) {
		return Context{}, ErrNotFound
	}
	return value, nil
}

func (r *valkeyRepository) mutate(
	ctx context.Context, change *mutation, clock func() time.Time,
) (MutationResult, error) {
	for attempt := 0; attempt < 128; attempt++ {
		result, committed, err := r.attempt(ctx, change, clock())
		if err != nil || committed {
			return result, err
		}
		if err := backoff(ctx, attempt); err != nil {
			return MutationResult{}, err
		}
	}
	return MutationResult{}, ErrBusy
}

func (r *valkeyRepository) attempt(ctx context.Context, change *mutation, now time.Time) (MutationResult, bool, error) {
	client, release := r.client.Dedicate()
	defer release()
	defer releaseWatch(client)
	keys := []string{recordKey(change.owner, change.id), receiptKey(change.owner, change.key),
		catalogKey(change.owner), expiryKey(change.owner)}
	if err := client.Do(ctx, client.B().Watch().Key(keys...).Build()).Error(); err != nil {
		return MutationResult{}, false, fmt.Errorf("watch memory context: %w", err)
	}
	if replay, found, err := readReceipt(ctx, client, change, now); found || err != nil {
		return replay, found, err
	}
	value, err := prepareChange(ctx, client, change, now)
	if err != nil {
		return MutationResult{}, false, err
	}
	if change.validate != nil {
		if err := change.validate(ctx); err != nil {
			return MutationResult{}, false, err
		}
	}
	if err := checkIndexes(ctx, client, change.owner); err != nil {
		return MutationResult{}, false, err
	}
	commands, err := mutationCommands(client, change, &value)
	if err != nil {
		return MutationResult{}, false, err
	}
	committed, err := execute(ctx, client, commands)
	if err != nil || !committed {
		return MutationResult{}, committed, err
	}
	result := MutationResult{Context: value, Operation: change.operation,
		CurrentState: "present", CurrentVersion: value.Version}
	if change.operation == "delete" {
		result.CurrentState, result.CurrentVersion = "absent", ""
	}
	return result, true, nil
}

func readReceipt(
	ctx context.Context, client rueidis.CoreClient, change *mutation, now time.Time,
) (MutationResult, bool, error) {
	raw, err := client.Do(ctx, client.B().Get().Key(receiptKey(change.owner, change.key)).Build()).ToString()
	if rueidis.IsRedisNil(err) {
		return MutationResult{}, false, nil
	}
	if err != nil {
		return MutationResult{}, false, fmt.Errorf("read context receipt: %w", err)
	}
	var saved receipt
	if len(raw) > MaxPayloadBytes || json.Unmarshal([]byte(raw), &saved) != nil || saved.Context.Owner != change.owner {
		return MutationResult{}, false, ErrCorrupt
	}
	if saved.Fingerprint != change.digest {
		return MutationResult{}, false, ErrIdempotencyConflict
	}
	result := MutationResult{Context: saved.Context, Operation: saved.Operation, Replayed: true, CurrentState: "absent"}
	current, err := readContext(ctx, client, change.owner, saved.Context.ID, now)
	if errors.Is(err, ErrNotFound) {
		return result, true, nil
	}
	if err != nil {
		return MutationResult{}, false, err
	}
	result.CurrentState, result.CurrentVersion = "present", current.Version
	return result, true, nil
}

func prepareChange(ctx context.Context, client rueidis.CoreClient, change *mutation, now time.Time) (Context, error) {
	current, err := readContext(ctx, client, change.owner, change.id, now)
	if err != nil && (change.operation != "create" || !errors.Is(err, ErrNotFound)) {
		return Context{}, err
	}
	if change.operation == "create" {
		if err == nil {
			return Context{}, &ConflictError{CurrentVersion: current.Version}
		}
	} else {
		if current.Version != change.expected {
			return Context{}, &ConflictError{CurrentVersion: current.Version}
		}
	}
	value := change.apply(current)
	if change.operation != "delete" && value.ExpiresAt != nil && !value.ExpiresAt.After(now) {
		return Context{}, fmt.Errorf("expiry must be in the future: %w", ErrInvalid)
	}
	return value, nil
}

func checkIndexes(ctx context.Context, client rueidis.CoreClient, owner string) error {
	for _, key := range []string{catalogKey(owner), expiryKey(owner)} {
		kind, err := client.Do(ctx, client.B().Type().Key(key).Build()).ToString()
		if err != nil {
			return fmt.Errorf("read context index type: %w", err)
		}
		if kind != "none" && kind != "zset" {
			return fmt.Errorf("context index has type %s: %w", kind, ErrCorrupt)
		}
	}
	return nil
}

func mutationCommands(client rueidis.CoreClient, change *mutation, value *Context) ([]rueidis.Completed, error) {
	saved, err := boundedJSON(receipt{Fingerprint: change.digest, Operation: change.operation, Context: *value})
	if err != nil {
		return nil, err
	}
	commands := make([]rueidis.Completed, 0, 4)
	if change.operation == "delete" {
		commands = append(commands,
			client.B().Del().Key(recordKey(change.owner, change.id)).Build(),
			client.B().Zrem().Key(catalogKey(change.owner)).Member(change.id).Build(),
			client.B().Zrem().Key(expiryKey(change.owner)).Member(change.id).Build())
	} else {
		commands, err = saveCommands(client, value)
		if err != nil {
			return nil, err
		}
	}
	commands = append(commands, client.B().Arbitrary("SET").Keys(receiptKey(change.owner, change.key)).
		Args(string(saved), "PX", strconv.FormatInt(ReceiptLifetime.Milliseconds(), 10)).Build())
	return commands, nil
}

func saveCommands(client rueidis.CoreClient, value *Context) ([]rueidis.Completed, error) {
	encoded, err := boundedJSON(value)
	if err != nil {
		return nil, err
	}
	args := []string{string(encoded)}
	if value.ExpiresAt != nil {
		args = append(args, "PXAT", strconv.FormatInt(value.ExpiresAt.UnixMilli(), 10))
	}
	commands := []rueidis.Completed{
		client.B().Arbitrary("SET").Keys(recordKey(value.Owner, value.ID)).Args(args...).Build(),
		client.B().Arbitrary("ZADD").Keys(catalogKey(value.Owner)).Args("0", value.ID).Build(),
	}
	if value.ExpiresAt == nil {
		commands = append(commands, client.B().Zrem().Key(expiryKey(value.Owner)).Member(value.ID).Build())
	} else {
		commands = append(commands, client.B().Arbitrary("ZADD").Keys(expiryKey(value.Owner)).
			Args(strconv.FormatInt(value.ExpiresAt.UnixMilli(), 10), value.ID).Build())
	}
	return commands, nil
}
