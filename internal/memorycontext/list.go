package memorycontext

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/redis/rueidis"
)

type listCursor struct {
	Owner   string `json:"owner"`
	After   string `json:"after"`
	Version int    `json:"v"`
}

func encodeCursor(owner, after string) string {
	// These values are plain strings and integers, so marshaling cannot fail.
	encoded, _ := json.Marshal(listCursor{Owner: ownerBase(owner), After: after, Version: 1})
	return base64.RawURLEncoding.EncodeToString(encoded)
}

func decodeCursor(owner, cursor string) (string, error) {
	if cursor == "" {
		return "", nil
	}
	if len(cursor) > 1024 {
		return "", fmt.Errorf("cursor too large: %w", ErrInvalid)
	}
	encoded, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return "", fmt.Errorf("invalid cursor encoding: %w", ErrInvalid)
	}
	var value listCursor
	if json.Unmarshal(encoded, &value) != nil || value.Version != 1 || value.Owner != ownerBase(owner) ||
		!identifier.MatchString(value.After) {
		return "", fmt.Errorf("invalid or foreign context cursor: %w", ErrInvalid)
	}
	return value.After, nil
}

func (r *valkeyRepository) list(ctx context.Context, owner, after string, limit int, now time.Time) (Page, error) {
	if err := r.cleanupExpired(ctx, owner); err != nil {
		return Page{}, err
	}
	minimum := "-"
	if after != "" {
		minimum = "(" + after
	}
	ids, err := r.client.Do(ctx, r.client.B().Arbitrary("ZRANGEBYLEX").Keys(catalogKey(owner)).
		Args(minimum, "+", "LIMIT", "0", strconv.Itoa(limit+1)).Build()).AsStrSlice()
	if err != nil {
		return Page{}, fmt.Errorf("list memory contexts: %w", err)
	}
	page := Page{Contexts: make([]Context, 0, min(limit, len(ids)))}
	if len(ids) > limit {
		ids = ids[:limit]
		page.NextCursor = encodeCursor(owner, ids[len(ids)-1])
	}
	if len(ids) == 0 {
		return page, nil
	}
	commands := make([]rueidis.Completed, len(ids))
	for i, id := range ids {
		if !identifier.MatchString(id) {
			return Page{}, ErrCorrupt
		}
		commands[i] = r.client.B().Get().Key(recordKey(owner, id)).Build()
	}
	replies := r.client.DoMulti(ctx, commands...)
	if len(replies) != len(ids) {
		return Page{}, fmt.Errorf("incomplete context page")
	}
	for i, reply := range replies {
		value, err := decodeListed(reply, owner, ids[i], now)
		if errors.Is(err, ErrNotFound) {
			continue
		}
		if err != nil {
			return Page{}, err
		}
		page.Contexts = append(page.Contexts, value)
	}
	return page, nil
}

func decodeListed(reply rueidis.RedisResult, owner, id string, now time.Time) (Context, error) {
	raw, err := reply.ToString()
	if rueidis.IsRedisNil(err) {
		return Context{}, ErrNotFound
	}
	if err != nil {
		return Context{}, fmt.Errorf("read listed context: %w", err)
	}
	return decodeContext(raw, owner, id, now)
}

// Each list removes at most 100 expired index entries. Records have matching
// native PXAT expiry. WATCH prevents a concurrent expiry extension or removal
// from losing its catalog entry. A page remains bounded even with a large backlog.
func (r *valkeyRepository) cleanupExpired(ctx context.Context, owner string) error {
	for attempt := 0; attempt < 8; attempt++ {
		committed, err := r.cleanupAttempt(ctx, owner)
		if err != nil || committed {
			return err
		}
		if err := backoff(ctx, attempt); err != nil {
			return err
		}
	}
	// Cleanup is advisory; listing itself still filters expired record values.
	return nil
}

func (r *valkeyRepository) cleanupAttempt(ctx context.Context, owner string) (bool, error) {
	client, release := r.client.Dedicate()
	defer release()
	defer releaseWatch(client)
	if err := client.Do(ctx, client.B().Watch().Key(expiryKey(owner), catalogKey(owner)).Build()).Error(); err != nil {
		return false, fmt.Errorf("watch context expiry cleanup: %w", err)
	}
	if err := checkIndexes(ctx, client, owner); err != nil {
		return false, err
	}
	// PXAT uses Valkey's clock. Use its time for index maintenance as well:
	// the application clock can lag a VM or another cluster host.
	clock, err := client.Do(ctx, client.B().Time().Build()).AsStrSlice()
	if err != nil {
		return false, fmt.Errorf("read context expiry clock: %w", err)
	}
	if len(clock) != 2 {
		return false, ErrCorrupt
	}
	seconds, err := strconv.ParseInt(clock[0], 10, 64)
	if err != nil {
		return false, fmt.Errorf("parse context expiry clock: %w", err)
	}
	micros, err := strconv.ParseInt(clock[1], 10, 64)
	if err != nil {
		return false, fmt.Errorf("parse context expiry clock: %w", err)
	}
	now := time.Unix(seconds, micros*1000)
	ids, err := client.Do(ctx, client.B().Arbitrary("ZRANGEBYSCORE").Keys(expiryKey(owner)).
		Args("-inf", strconv.FormatInt(now.UnixMilli(), 10), "LIMIT", "0", "100").Build()).AsStrSlice()
	if err != nil {
		return false, fmt.Errorf("read expired contexts: %w", err)
	}
	if len(ids) == 0 {
		return true, nil
	}
	return execute(ctx, client, []rueidis.Completed{
		client.B().Zrem().Key(catalogKey(owner)).Member(ids...).Build(),
		client.B().Zrem().Key(expiryKey(owner)).Member(ids...).Build(),
	})
}
