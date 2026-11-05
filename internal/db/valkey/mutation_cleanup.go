package valkey

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"

	"github.com/redis/rueidis"
)

var errWorkConflict = errors.New("mutation work changed concurrently")

func (s *Store) recoverMutationWork(
	ctx context.Context, queue, id string, now int64, force bool,
) (*mutationWork, error) {
	for attempt := 0; attempt < maxTransactionAttempts; attempt++ {
		work, changed, err := s.fenceMutationWork(ctx, queue, id, now, force)
		if err != nil {
			return nil, err
		}
		if !changed {
			if err := transactionRetry(ctx, attempt); err != nil {
				return nil, err
			}
			continue
		}
		if work == nil || work.State != "cleaning" {
			return work, nil
		}
		conflicts := 0
		for work.State == "cleaning" {
			next, err := s.cleanupWorkBatch(ctx, queue, work)
			if err != nil && !errors.Is(err, errWorkConflict) {
				return work, err
			}
			if next != nil {
				work = next
				conflicts = 0
			} else {
				conflicts++
				if conflicts >= maxTransactionAttempts {
					return work, fmt.Errorf("mutation cleanup contention")
				}
				if err := transactionRetry(ctx, conflicts); err != nil {
					return work, err
				}
			}
			if work.State == "cleaning" {
				if err := ctx.Err(); err != nil {
					return work, fmt.Errorf("mutation cleanup canceled: %w", err)
				}
			}
		}
		return work, nil
	}
	return nil, fmt.Errorf("mutation recovery contention")
}

func (s *Store) fenceMutationWork(
	ctx context.Context, queue, id string, now int64, force bool,
) (*mutationWork, bool, error) {
	client, release := s.client.Dedicate()
	defer release()
	defer unwatch(client)
	if err := watchKeys(ctx, client, workKey(queue, id)); err != nil {
		return nil, false, err
	}
	work, err := readMutationWork(ctx, client, queue, id)
	if rueidis.IsRedisNil(err) || errors.Is(err, errWorkRegistering) {
		// Registration can expire before it was persisted; no payload was allowed.
		_, committed, err := execTransaction(ctx, client,
			[]rueidis.Completed{client.B().Del().Key(workKey(queue, id)).Build(),
				client.B().Zrem().Key(queue).Member(id).Build()})
		return nil, committed, err
	}
	if err != nil {
		return nil, false, err
	}
	if work.State != "preparing" && work.State != "committed" {
		return &work, true, nil
	}
	if work.State == "preparing" && !force && work.Deadline > now {
		return &work, true, nil
	}
	work.State = "cleaning"
	update := mutationWorkCommand(client, queue, &work)
	_, committed, err := execTransaction(ctx, client, []rueidis.Completed{
		update, client.B().Zadd().Key(queue).ScoreMember().ScoreMember(0, id).Build(),
	})
	return &work, committed, err
}

func (s *Store) cleanupWorkBatch(ctx context.Context, queue string, snapshot *mutationWork) (*mutationWork, error) {
	work := *snapshot
	id := work.ID
	client, release := s.client.Dedicate()
	defer release()
	defer unwatch(client)
	if err := watchKeys(ctx, client, workKey(queue, id)); err != nil {
		return nil, err
	}
	if err := readMutationWorkHeader(ctx, client, queue, &work); err != nil {
		return nil, err
	}
	if work.State != "cleaning" {
		return &work, nil
	}
	keys, owner := work.New, work.ID
	if work.Committed {
		keys, owner = work.Old, work.OldOwner
	}
	if work.Progress < 0 || work.Progress > len(keys) {
		return nil, fmt.Errorf("invalid mutation cleanup progress")
	}
	if len(keys) > 0 && owner == "" {
		return nil, fmt.Errorf("mutation cleanup owner is missing")
	}
	if work.Progress == len(keys) {
		return finishMutationWork(ctx, client, queue, &work)
	}
	end := min(work.Progress+workBatchSize, len(keys))
	batch := keys[work.Progress:end]
	if err := watchKeys(ctx, client, batch...); err != nil {
		return nil, err
	}
	commands, err := ownedCleanupCommands(ctx, client, batch, owner)
	if err != nil {
		return nil, err
	}
	work.Progress = end
	update := mutationWorkCommand(client, queue, &work)
	commands = append(commands, update)
	_, committed, err := execTransaction(ctx, client, commands)
	if !committed && err == nil {
		return nil, errWorkConflict
	}
	return &work, err
}

func ownedCleanupCommands(
	ctx context.Context, client rueidis.DedicatedClient, keys []string, owner string,
) ([]rueidis.Completed, error) {
	reads := make([]rueidis.Completed, len(keys))
	for i, key := range keys {
		reads[i] = client.B().Hget().Key(key).Field("__prepared_by").Build()
	}
	replies := client.DoMulti(ctx, reads...)
	if len(replies) != len(keys) {
		return nil, fmt.Errorf("cleanup owner response count mismatch")
	}
	commands := make([]rueidis.Completed, 0, len(keys)+1)
	for i, reply := range replies {
		actual, err := reply.ToString()
		if rueidis.IsRedisNil(err) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("read fragment cleanup owner: %w", err)
		}
		if actual == owner {
			commands = append(commands, client.B().Unlink().Key(keys[i]).Build())
		}
	}
	return commands, nil
}

func finishMutationWork(
	ctx context.Context, client rueidis.DedicatedClient, queue string, work *mutationWork,
) (*mutationWork, error) {
	work.State = "aborted"
	if work.Committed {
		work.State = "complete"
	}
	work.New, work.Old, work.Staging, work.OldOwner = nil, nil, "", ""
	work.GuardKey, work.GuardGeneration, work.GuardState = "", "", ""
	work.Progress, work.Deadline = 0, 0
	encoded, err := json.Marshal(work)
	if err != nil {
		return nil, fmt.Errorf("encode mutation receipt: %w", err)
	}
	_, committed, err := execTransaction(ctx, client, []rueidis.Completed{
		client.B().Hset().Key(workKey(queue, work.ID)).FieldValue().FieldValue("payload", string(encoded)).
			FieldValue("state", work.State).FieldValue("progress", "0").
			FieldValue("committed", strconv.FormatBool(work.Committed)).FieldValue("deadline", "0").Build(),
		client.B().Pexpire().Key(workKey(queue, work.ID)).Milliseconds(mutationReceiptLifetime.Milliseconds()).Build(),
		client.B().Zrem().Key(queue).Member(work.ID).Build(),
	})
	if !committed && err == nil {
		return nil, errWorkConflict
	}
	return work, err
}
