package valkey

import (
	"context"
	"fmt"
	"strconv"

	"github.com/kailas-cloud/vecdex/internal/db"
	"github.com/redis/rueidis"
)

// CatalogIDs reads IDs strictly after the previous page's last ID. All scores
// are zero, so lexicographic ordering is independent of concurrent insert/delete.
func (s *Store) CatalogIDs(ctx context.Context, key, after string, limit int) ([]string, error) {
	if limit <= 0 {
		return nil, fmt.Errorf("catalog page size must be positive")
	}
	minimum := "-"
	if after != "" {
		minimum = "(" + after
	}
	command := s.b().Arbitrary("ZRANGEBYLEX").Keys(key).Args(minimum, "+", "LIMIT", "0", strconv.Itoa(limit)).Build()
	ids, err := s.do(ctx, command).AsStrSlice()
	if err != nil {
		return nil, &db.Error{Op: "ZRANGEBYLEX", Err: err}
	}
	return ids, nil
}

// CatalogCount is an immediate parent count, independent of index backfill.
func (s *Store) CatalogCount(ctx context.Context, key string) (int, error) {
	value, err := s.do(ctx, s.b().Zcard().Key(key).Build()).AsInt64()
	if err != nil {
		return 0, &db.Error{Op: "ZCARD", Err: err}
	}
	return int(value), nil
}

func mutationCommitCommands(
	client rueidis.DedicatedClient, m *db.HashMutation, stagingKey, version string,
) []rueidis.Completed {
	commands := make([]rueidis.Completed, 0, 3)
	if m.Delete {
		commands = append(commands, client.B().Unlink().Key(m.Key).Build())
	} else {
		commands = append(commands, client.B().Rename().Key(stagingKey).Newkey(m.Key).Build(),
			client.B().Persist().Key(m.Key).Build())
	}
	if m.CatalogKey != "" {
		name := "ZADD"
		arguments := []string{"0", m.CatalogMember}
		if m.Delete {
			name = "ZREM"
			arguments = []string{m.CatalogMember}
		}
		commands = append(commands, client.B().Arbitrary(name).Keys(m.CatalogKey).Args(arguments...).Build())
	}
	if m.DeadlineKey != "" {
		if m.Delete {
			commands = append(commands, client.B().Zrem().Key(m.DeadlineKey).Member(m.CatalogMember).Build())
		} else {
			score := "+inf"
			if m.ExpiresAt > 0 {
				score = strconv.FormatInt(m.ExpiresAt, 10)
			}
			commands = append(commands, client.B().Arbitrary("ZADD").Keys(m.DeadlineKey).
				Args(score, m.CatalogMember).Build())
		}
	}
	if m.ExpiryMarkerKey != "" {
		if m.Delete || m.ExpiresAt == 0 {
			commands = append(commands, client.B().Unlink().Key(m.ExpiryMarkerKey).Build())
		} else {
			commands = append(commands, client.B().Arbitrary("SET").Keys(m.ExpiryMarkerKey).
				Args(version, "PXAT", strconv.FormatInt(m.ExpiresAt, 10)).Build())
		}
	}
	if m.LifecycleKey != "" {
		name := "ZREM"
		args := []string{m.LifecycleMember}
		if m.LifecyclePending {
			name = "ZADD"
			args = []string{"0", m.LifecycleMember}
		}
		commands = append(commands, client.B().Arbitrary(name).Keys(m.LifecycleKey).Args(args...).Build())
	}
	return commands
}

func checkCatalogType(ctx context.Context, client rueidis.DedicatedClient, key string) error {
	if key == "" {
		return nil
	}
	kind, err := client.Do(ctx, client.B().Type().Key(key).Build()).ToString()
	if err != nil {
		return fmt.Errorf("read catalog type: %w", err)
	}
	if kind != "none" && kind != "zset" {
		return fmt.Errorf("invalid document catalog type %s", kind)
	}
	return nil
}

// CatalogIDPages pipelines bounded ID windows on the actual owners of each slot.
func (s *Store) CatalogIDPages(ctx context.Context, keys []string, after string, limit int) ([][]string, error) {
	if limit <= 0 {
		return nil, fmt.Errorf("catalog page size must be positive")
	}
	minimum := "-"
	if after != "" {
		minimum = "(" + after
	}
	commands := make([]rueidis.Completed, len(keys))
	for i, key := range keys {
		commands[i] = s.b().Arbitrary("ZRANGEBYLEX").Keys(key).
			Args(minimum, "+", "LIMIT", "0", strconv.Itoa(limit)).Build()
	}
	replies := s.client.DoMulti(ctx, commands...)
	if len(replies) != len(keys) {
		return nil, fmt.Errorf("catalog page response count mismatch")
	}
	pages := make([][]string, len(keys))
	for i, reply := range replies {
		ids, err := reply.AsStrSlice()
		if err != nil {
			return nil, &db.Error{Op: "ZRANGEBYLEX", Err: err}
		}
		pages[i] = ids
	}
	return pages, nil
}

// CatalogCounts reads partition cardinalities without a shared write-side counter.
func (s *Store) CatalogCounts(ctx context.Context, keys []string) ([]int, error) {
	commands := make([]rueidis.Completed, len(keys))
	for i, key := range keys {
		commands[i] = s.b().Zcard().Key(key).Build()
	}
	replies := s.client.DoMulti(ctx, commands...)
	if len(replies) != len(keys) {
		return nil, fmt.Errorf("catalog count response count mismatch")
	}
	counts := make([]int, len(keys))
	for i, reply := range replies {
		count, err := reply.AsInt64()
		if err != nil {
			return nil, &db.Error{Op: "ZCARD", Err: err}
		}
		if count < 0 || uint64(count) > uint64(^uint(0)>>1) {
			return nil, fmt.Errorf("invalid catalog count")
		}
		counts[i] = int(count)
	}
	return counts, nil
}
