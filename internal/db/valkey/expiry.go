package valkey

import (
	"context"
	"fmt"
	"strconv"

	"github.com/kailas-cloud/vecdex/internal/db"
	domdoc "github.com/kailas-cloud/vecdex/internal/domain/document"
	"github.com/redis/rueidis"
)

func checkExpiryMarker(
	ctx context.Context, client rueidis.DedicatedClient, m *db.HashMutation, parent map[string]string,
) (bool, error) {
	if m.ExpiryMarkerKey == "" {
		return false, nil
	}
	expiresAt, err := domdoc.ParseExpiresAt(parent["__expires_at"])
	if err != nil {
		return false, fmt.Errorf("read source expiration: %w", err)
	}
	if expiresAt == 0 {
		return false, nil
	}
	marker, err := client.Do(ctx, client.B().Get().Key(m.ExpiryMarkerKey).Build()).ToString()
	if rueidis.IsRedisNil(err) {
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("read source expiry marker: %w", err)
	}
	if marker != parent["__source_version"] {
		return false, errPreparedChildBusy // A replacement won between the two reads; retry before preparing.
	}
	return false, nil
}

// ExpiryIDs discovers only a bounded due window, independently of persistent sources.
func (s *Store) ExpiryIDs(ctx context.Context, key string, before int64, limit int) ([]string, error) {
	pages, err := s.ExpiryIDPages(ctx, []string{key}, before, limit)
	if err != nil {
		return nil, err
	}
	return pages[0], nil
}

// ExpiryIDPages pipelines due windows on the owners of their partition slots.
func (s *Store) ExpiryIDPages(ctx context.Context, keys []string, before int64, limit int) ([][]string, error) {
	if before <= 0 || limit <= 0 {
		return nil, fmt.Errorf("invalid expiry work window")
	}
	commands := make([]rueidis.Completed, len(keys))
	for i, key := range keys {
		commands[i] = s.b().Arbitrary("ZRANGEBYSCORE").Keys(key).
			Args("-inf", strconv.FormatInt(before, 10), "LIMIT", "0", strconv.Itoa(limit)).Build()
	}
	replies := s.client.DoMulti(ctx, commands...)
	if len(replies) != len(keys) {
		return nil, fmt.Errorf("expiry window response count mismatch")
	}
	pages := make([][]string, len(keys))
	for i, reply := range replies {
		ids, err := reply.AsStrSlice()
		if err != nil {
			return nil, &db.Error{Op: "ZRANGEBYSCORE", Err: err}
		}
		pages[i] = ids
	}
	return pages, nil
}

// LiveCounts counts non-expired sources even while physical cleanup is stopped.
// Each routed ZCOUNT is atomic; the collection sum, like catalog pagination, may
// observe independent concurrent changes in different partitions.
func (s *Store) LiveCounts(ctx context.Context, keys []string, now int64) ([]int, error) {
	commands := make([]rueidis.Completed, len(keys))
	for i, key := range keys {
		commands[i] = s.b().Arbitrary("ZCOUNT").Keys(key).Args("("+strconv.FormatInt(now, 10), "+inf").Build()
	}
	replies := s.client.DoMulti(ctx, commands...)
	if len(replies) != len(keys) {
		return nil, fmt.Errorf("live count response count mismatch")
	}
	counts := make([]int, len(keys))
	for i, reply := range replies {
		count, err := reply.AsInt64()
		if err != nil {
			return nil, &db.Error{Op: "ZCOUNT", Err: err}
		}
		if count < 0 || uint64(count) > uint64(^uint(0)>>1) {
			return nil, fmt.Errorf("invalid live source count")
		}
		counts[i] = int(count)
	}
	return counts, nil
}
