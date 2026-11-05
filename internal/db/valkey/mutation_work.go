package valkey

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/kailas-cloud/vecdex/internal/db"
	"github.com/redis/rueidis"
)

const (
	preparedWorkLifetime    = 10 * time.Minute
	mutationReceiptLifetime = 24 * time.Hour
	workBatchSize           = 32
)

var (
	errMutationFenced  = db.ErrMutationFenced
	errWorkRegistering = errors.New("mutation registration has not published its journal")
)

// mutationWork is persisted before any fragment is created. New and old keys
// remain reachable across process death and ambiguous EXEC replies.
type mutationWork struct {
	GuardKey        string                `json:"guard_key,omitempty"`
	GuardGeneration string                `json:"guard_generation,omitempty"`
	GuardState      string                `json:"guard_state,omitempty"`
	OldOwner        string                `json:"old_owner,omitempty"`
	ID              string                `json:"id"`
	State           string                `json:"state"`
	Parent          string                `json:"parent"`
	Staging         string                `json:"staging,omitempty"`
	New             []string              `json:"new,omitempty"`
	Old             []string              `json:"old,omitempty"`
	Deadline        int64                 `json:"deadline"`
	Progress        int                   `json:"progress"`
	Committed       bool                  `json:"committed"`
	Result          db.HashMutationResult `json:"result"`
}

func workKey(queue, id string) string { return queue + ":operation:" + id }

func (s *Store) commitDurableMutation(
	ctx context.Context, client rueidis.DedicatedClient, m *db.HashMutation,
	previous map[string]string, old []string, result db.HashMutationResult,
) (db.HashMutationResult, bool, error) {
	work, fields, err := newMutationWork(m, previous, old, result)
	if err != nil {
		return result, false, err
	}
	if err := s.registerMutationWork(ctx, m.WorkQueueKey, &work); err != nil {
		return result, false, err
	}
	if !m.Delete {
		if err := s.prepareMutationWork(ctx, m, &work, fields); err != nil {
			s.recoverFailedWork(m.WorkQueueKey, work.ID)
			return result, false, err
		}
	}
	if err := watchKeys(ctx, client, workKey(m.WorkQueueKey, work.ID)); err != nil {
		s.recoverFailedWork(m.WorkQueueKey, work.ID)
		return result, false, err
	}
	current, err := readMutationWork(ctx, client, m.WorkQueueKey, work.ID)
	if err != nil {
		return result, false, err
	}
	if current.State != "preparing" || current.Deadline <= time.Now().UnixMilli() {
		s.recoverFailedWork(m.WorkQueueKey, work.ID)
		return result, false, errMutationFenced
	}
	work.State, work.Committed = "committed", true
	commands := mutationCommitCommands(client, m, work.Staging, work.Result.Version)
	journal := mutationWorkCommand(client, m.WorkQueueKey, &work)
	commands = append(commands, journal,
		client.B().Zadd().Key(m.WorkQueueKey).ScoreMember().ScoreMember(0, work.ID).Build())
	if m.Request != nil {
		receipt, receiptErr := requestCommitCommands(client, m.Request, work.Result)
		if receiptErr != nil {
			return result, false, receiptErr
		}
		commands = append(commands, receipt...)
	}
	_, committed, err := execTransaction(ctx, client, commands)
	if committed {
		s.recoverFailedWork(m.WorkQueueKey, work.ID)
		return work.Result, true, nil
	}
	recovered := s.recoverFailedWork(m.WorkQueueKey, work.ID)
	if recovered != nil && recovered.Committed {
		return recovered.Result, true, nil
	}
	return result, false, err
}

func newMutationWork(
	m *db.HashMutation, previous map[string]string, old []string, result db.HashMutationResult,
) (mutationWork, map[string]string, error) {
	work := mutationWork{ID: rand.Text(), State: "preparing", Parent: m.Key, OldOwner: previous["__mutation_id"],
		Deadline: time.Now().Add(preparedWorkLifetime).UnixMilli(), Result: result,
		GuardKey: m.GuardKey, GuardGeneration: m.GuardGeneration, GuardState: m.GuardState}
	work.Result.OperationID = work.ID
	if len(m.Children) > 0 || m.Replace || m.Delete {
		work.Old = old
	}
	if m.Delete {
		return work, nil, nil
	}
	work.Result.Revision++
	work.Staging = m.StagingKey
	if work.Staging == "" {
		work.Staging = m.Key + ":pending"
	}
	work.Staging += ":" + work.ID
	work.New = make([]string, 0, len(m.Children)+1)
	for _, child := range m.Children {
		work.New = append(work.New, child.Key)
	}
	work.New = append(work.New, work.Staging)
	fields, err := replacementFields(m, previous, work.Result.Revision, work.ID)
	if err != nil {
		return work, nil, err
	}
	incarnation := previous["__incarnation"]
	if incarnation == "" || result.Created {
		incarnation = work.ID
	}
	if m.DeadlineKey != "" {
		fields["__expires_at"] = strconv.FormatInt(m.ExpiresAt, 10)
	}
	fields["__incarnation"] = incarnation
	digest := sha256.Sum256([]byte(incarnation + ":" + strconv.Itoa(work.Result.Revision)))
	work.Result.Version = base64.RawURLEncoding.EncodeToString(digest[:])
	fields["__source_version"] = work.Result.Version
	return work, fields, nil
}

func (s *Store) registerMutationWork(ctx context.Context, queue string, work *mutationWork) error {
	encoded, err := json.Marshal(work)
	if err != nil {
		return fmt.Errorf("encode mutation work: %w", err)
	}
	key := workKey(queue, work.ID)
	// The temporary intent expires if registration fails. No payload can be
	// written until the queue entry exists and the intent is made persistent.
	command := s.b().Set().Key(key).Value(string(encoded)).Nx().Px(preparedWorkLifetime).Build()
	if err := s.do(ctx, command).Error(); err != nil {
		return fmt.Errorf("prepare mutation intent: %w", err)
	}
	if err := s.registerMutationMembership(ctx, queue, work, string(encoded)); err != nil {
		return err
	}
	return s.publishMutationIntent(ctx, queue, work, string(encoded))
}

func (s *Store) publishMutationIntent(ctx context.Context, queue string, work *mutationWork, encoded string) error {
	client, release := s.client.Dedicate()
	defer release()
	defer unwatch(client)
	key := workKey(queue, work.ID)
	if err := watchRegistrationFence(ctx, client, queue, work); err != nil {
		return err
	}
	current, err := client.Do(ctx, client.B().Get().Key(key).Build()).ToString()
	if err != nil {
		return fmt.Errorf("read registering mutation: %w", err)
	}
	if current != encoded || work.Deadline <= time.Now().UnixMilli() {
		return errMutationFenced
	}
	// Converting the expiring reservation to a persistent journal happens only
	// after queue registration. A failed conversion cannot leave payload behind.
	_, committed, err := execTransaction(ctx, client, []rueidis.Completed{
		client.B().Del().Key(key).Build(),
		client.B().Hset().Key(key).FieldValue().FieldValue("payload", encoded).
			FieldValue("state", "preparing").FieldValue("progress", "0").
			FieldValue("committed", "false").FieldValue("deadline", strconv.FormatInt(work.Deadline, 10)).Build(),
	})
	if err != nil {
		return err
	}
	if !committed {
		return errMutationFenced
	}
	return nil
}

func (s *Store) prepareMutationWork(
	ctx context.Context, m *db.HashMutation, work *mutationWork, parent map[string]string,
) error {
	items := make([]db.HashSetItem, 0, len(m.Children)+1)
	items = append(items, m.Children...)
	items = append(items, db.HashSetItem{Key: work.Staging, Fields: parent})
	for start := 0; start < len(items); start += workBatchSize {
		end := min(start+workBatchSize, len(items))
		if err := s.prepareWorkBatch(ctx, m, work, items[start:end]); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) prepareWorkBatch(
	ctx context.Context, m *db.HashMutation, work *mutationWork, items []db.HashSetItem,
) error {
	client, release := s.client.Dedicate()
	defer release()
	defer unwatch(client)
	keys := []string{workKey(m.WorkQueueKey, work.ID)}
	if m.GuardGeneration != "" {
		keys = append(keys, m.GuardKey)
	}
	for _, item := range items {
		keys = append(keys, item.Key)
	}
	if err := watchKeys(ctx, client, keys...); err != nil {
		return err
	}
	current := *work
	if err := readMutationWorkHeader(ctx, client, m.WorkQueueKey, &current); err != nil {
		return err
	}
	if current.State != "preparing" || current.Deadline <= time.Now().UnixMilli() {
		return errMutationFenced
	}
	if err := checkMutationGuard(ctx, client, m); err != nil {
		return err
	}
	if err := checkPreparedKeys(ctx, client, items); err != nil {
		return err
	}
	commands := make([]rueidis.Completed, 0, len(items))
	for _, item := range items {
		command := client.B().Hset().Key(item.Key).FieldValue()
		for key, value := range item.Fields {
			command = command.FieldValue(key, value)
		}
		commands = append(commands, command.FieldValue("__prepared_by", work.ID).Build())
	}
	_, committed, err := execTransaction(ctx, client, commands)
	if err != nil {
		return err
	}
	if !committed {
		return errMutationFenced
	}
	return nil
}

func checkPreparedKeys(ctx context.Context, client rueidis.DedicatedClient, items []db.HashSetItem) error {
	commands := make([]rueidis.Completed, len(items))
	for i, item := range items {
		commands[i] = client.B().Exists().Key(item.Key).Build()
	}
	replies := client.DoMulti(ctx, commands...)
	if len(replies) != len(items) {
		return fmt.Errorf("prepared key response count mismatch")
	}
	for _, reply := range replies {
		exists, err := reply.AsInt64()
		if err != nil {
			return fmt.Errorf("check prepared key: %w", err)
		}
		if exists != 0 {
			return errPreparedChildBusy
		}
	}
	return nil
}

func readMutationWork(ctx context.Context, client rueidis.DedicatedClient, queue, id string) (mutationWork, error) {
	fields, err := client.Do(ctx, client.B().Hgetall().Key(workKey(queue, id)).Build()).AsStrMap()
	if isRedisErr(err, "WRONGTYPE") {
		return mutationWork{}, errWorkRegistering
	}
	if err != nil {
		return mutationWork{}, fmt.Errorf("read mutation intent: %w", err)
	}
	if len(fields) == 0 {
		return mutationWork{}, rueidis.Nil
	}
	var work mutationWork
	if err := json.Unmarshal([]byte(fields["payload"]), &work); err != nil {
		return work, fmt.Errorf("decode mutation intent: %w", err)
	}
	if work.ID != id {
		return work, fmt.Errorf("mutation intent identity mismatch")
	}
	header := []string{fields["state"], fields["progress"], fields["committed"], fields["deadline"]}
	err = applyMutationHeader(&work, header)
	return work, err
}

func readMutationWorkHeader(
	ctx context.Context, client rueidis.DedicatedClient, queue string, work *mutationWork,
) error {
	fields, err := client.Do(ctx, client.B().Hmget().Key(workKey(queue, work.ID)).
		Field("state", "progress", "committed", "deadline").Build()).AsStrSlice()
	if err != nil {
		return fmt.Errorf("read mutation header: %w", err)
	}
	return applyMutationHeader(work, fields)
}

func applyMutationHeader(work *mutationWork, fields []string) error {
	if len(fields) != 4 || fields[0] == "" {
		return fmt.Errorf("mutation header is missing")
	}
	progress, err := strconv.Atoi(fields[1])
	if err != nil {
		return fmt.Errorf("decode mutation progress: %w", err)
	}
	committed, err := strconv.ParseBool(fields[2])
	if err != nil {
		return fmt.Errorf("decode mutation outcome: %w", err)
	}
	deadline, err := strconv.ParseInt(fields[3], 10, 64)
	if err != nil {
		return fmt.Errorf("decode mutation deadline: %w", err)
	}
	if progress < 0 || deadline < 0 {
		return fmt.Errorf("invalid mutation progress or deadline")
	}
	switch fields[0] {
	case "preparing", "aborted":
		if committed {
			return fmt.Errorf("invalid uncommitted mutation state")
		}
	case "committed", "complete":
		if !committed {
			return fmt.Errorf("invalid committed mutation state")
		}
	case "cleaning":
	default:
		return fmt.Errorf("unknown mutation state %q", fields[0])
	}
	work.State, work.Progress, work.Committed, work.Deadline = fields[0], progress, committed, deadline
	return nil
}

// Only the small mutable header changes during commit/cleanup. The potentially
// large manifest is written at registration and compacted once at completion.
func mutationWorkCommand(client rueidis.DedicatedClient, queue string, work *mutationWork) rueidis.Completed {
	return client.B().Hset().Key(workKey(queue, work.ID)).FieldValue().
		FieldValue("state", work.State).FieldValue("progress", strconv.Itoa(work.Progress)).
		FieldValue("committed", strconv.FormatBool(work.Committed)).
		FieldValue("deadline", strconv.FormatInt(work.Deadline, 10)).Build()
}

func (s *Store) recoverFailedWork(queue, id string) *mutationWork {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	work, _ := s.recoverMutationWork(ctx, queue, id, time.Now().UnixMilli(), true)
	return work
}

// RecoverMutations fences expired preparations and retries acknowledged cleanup
// batches. The queue survives process restarts; no keyspace scan is required.
func (s *Store) RecoverMutations(ctx context.Context, queue string, limit int) (int, error) {
	if limit <= 0 {
		return 0, fmt.Errorf("mutation recovery limit must be positive")
	}
	now := time.Now().UnixMilli()
	ids, err := s.do(ctx, s.b().Arbitrary("ZRANGEBYSCORE").Keys(queue).
		Args("-inf", strconv.FormatInt(now, 10), "LIMIT", "0", strconv.Itoa(limit)).Build()).AsStrSlice()
	if err != nil {
		return 0, fmt.Errorf("read mutation recovery queue: %w", err)
	}
	completed := 0
	for _, id := range ids {
		work, err := s.recoverMutationWork(ctx, queue, id, now, false)
		if err != nil {
			return completed, err
		}
		if work != nil && (work.State == "complete" || work.State == "aborted") {
			completed++
		}
	}
	return completed, nil
}

// CancelMutations drains registered work after all relevant generation guards
// have been closed. Journal WATCHes fence concurrent stale publishers.
func (s *Store) CancelMutations(ctx context.Context, queue string, limit int) (int, error) {
	if limit <= 0 {
		return 0, fmt.Errorf("mutation cancellation limit must be positive")
	}
	ids, err := s.do(ctx, s.b().Arbitrary("ZRANGE").Keys(queue).
		Args("0", strconv.Itoa(limit-1)).Build()).AsStrSlice()
	if err != nil {
		return 0, fmt.Errorf("read pending mutations: %w", err)
	}
	completed := 0
	for _, id := range ids {
		if _, err := s.recoverMutationWork(ctx, queue, id, time.Now().UnixMilli(), true); err != nil {
			return completed, err
		}
		completed++
	}
	return completed, nil
}

// Queue registration and persistent publication both watch the generation
// guard, so a late writer cannot recreate work after final collection cleanup.
func watchRegistrationFence(
	ctx context.Context,
	client rueidis.DedicatedClient,
	queue string,
	work *mutationWork,
) error {
	keys := []string{workKey(queue, work.ID)}
	if work.GuardGeneration != "" {
		keys = append(keys, work.GuardKey)
	}
	if err := watchKeys(ctx, client, keys...); err != nil {
		return err
	}
	return checkMutationGuard(ctx, client, &db.HashMutation{
		GuardKey: work.GuardKey, GuardGeneration: work.GuardGeneration, GuardState: work.GuardState,
	})
}

func (s *Store) registerMutationMembership(
	ctx context.Context,
	queue string,
	work *mutationWork,
	encoded string,
) error {
	client, release := s.client.Dedicate()
	defer release()
	defer unwatch(client)
	if err := watchRegistrationFence(ctx, client, queue, work); err != nil {
		return err
	}
	current, err := client.Do(ctx, client.B().Get().Key(workKey(queue, work.ID)).Build()).ToString()
	if err != nil {
		return fmt.Errorf("read registering mutation: %w", err)
	}
	if current != encoded || work.Deadline <= time.Now().UnixMilli() {
		return errMutationFenced
	}
	_, committed, err := execTransaction(ctx, client, []rueidis.Completed{
		client.B().Zadd().Key(queue).ScoreMember().ScoreMember(float64(work.Deadline), work.ID).Build(),
	})
	if err != nil {
		return err
	}
	if !committed {
		return errMutationFenced
	}
	return nil
}
