package valkey

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/kailas-cloud/vecdex/internal/db"
	"github.com/kailas-cloud/vecdex/internal/domain"
	domdoc "github.com/kailas-cloud/vecdex/internal/domain/document"
	"github.com/redis/rueidis"
)

var errPreparedChildBusy = errors.New("fragment key is already owned by another write")

// MutateHash prepares immutable hashes outside the transaction. WATCH protects
// the parent and collection generation; the commit uses RENAME and PERSIST.
func (s *Store) MutateHash(ctx context.Context, m *db.HashMutation) (db.HashMutationResult, error) {
	if err := validateDeadlineMutation(m); err != nil {
		return db.HashMutationResult{}, err
	}
	if m.Request != nil && m.WorkQueueKey == "" {
		return db.HashMutationResult{}, fmt.Errorf("idempotent writes require durable mutation work")
	}
	if (m.CatalogKey == "") != (m.CatalogMember == "") {
		return db.HashMutationResult{}, fmt.Errorf("catalog key and member must be supplied together")
	}
	for attempt := 0; attempt < maxTransactionAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return db.HashMutationResult{}, fmt.Errorf("document write canceled: %w", err)
		}
		result, committed, err := s.mutateHashAttempt(ctx, m)
		if errors.Is(err, errPreparedChildBusy) || canRetryTransaction(err) {
			err = nil
		}
		if err != nil || committed {
			return result, err
		}
		if err := transactionRetry(ctx, attempt); err != nil {
			return result, err
		}
	}
	return db.HashMutationResult{}, fmt.Errorf("document transaction contention: %w", domain.ErrRevisionConflict)
}

func (s *Store) mutateHashAttempt(ctx context.Context, m *db.HashMutation) (db.HashMutationResult, bool, error) {
	client, release := s.client.Dedicate()
	defer release()
	defer unwatch(client)
	if err := watchKeys(ctx, client, mutationWatchKeys(m)...); err != nil {
		return db.HashMutationResult{}, false, err
	}
	if m.Request != nil {
		request, err := checkWriteRequest(ctx, client, m.Request)
		if err != nil || request.Replay {
			return request.Result, request.Replay, err
		}
	}
	if err := checkCatalogType(ctx, client, m.CatalogKey); err != nil {
		return db.HashMutationResult{}, false, err
	}
	if err := checkCatalogType(ctx, client, m.DeadlineKey); err != nil {
		return db.HashMutationResult{}, false, err
	}
	if err := checkCatalogType(ctx, client, m.LifecycleKey); err != nil {
		return db.HashMutationResult{}, false, err
	}
	previous, err := readMutationParent(ctx, client, m)
	if err != nil {
		return db.HashMutationResult{}, false, err
	}
	markerExpired, err := checkExpiryMarker(ctx, client, m, previous)
	if err != nil {
		return db.HashMutationResult{}, false, err
	}
	revision, exists, err := checkMutation(m, previous, markerExpired)
	if err != nil {
		return db.HashMutationResult{}, false, err
	}
	oldChildren, err := mutationChildren(previous)
	if err != nil {
		return db.HashMutationResult{}, false, err
	}
	result := db.HashMutationResult{Revision: revision, Created: !exists, Version: previous["__source_version"]}
	if m.WorkQueueKey != "" {
		return s.commitDurableMutation(ctx, client, m, previous, oldChildren, result)
	}
	if m.Delete {
		return s.commitHashDeletion(ctx, client, m, oldChildren, result)
	}
	result.Revision++
	return s.commitHashReplacement(ctx, client, m, previous, oldChildren, result)
}

func mutationWatchKeys(m *db.HashMutation) []string {
	keys := []string{m.Key}
	if m.ExpiryMarkerKey != "" {
		keys = append(keys, m.ExpiryMarkerKey)
	}
	if m.Request != nil {
		keys = append(keys, m.Request.Key)
	}
	if m.GuardGeneration != "" {
		keys = append(keys, m.GuardKey)
	}
	return keys
}

func (s *Store) commitHashDeletion(
	ctx context.Context, client rueidis.DedicatedClient, m *db.HashMutation, children []string,
	result db.HashMutationResult,
) (db.HashMutationResult, bool, error) {
	_, committed, err := execTransaction(ctx, client, mutationCommitCommands(client, m, "", ""))
	if committed {
		s.unlinkHashes(children)
	}
	return result, committed, err
}

func readMutationParent(
	ctx context.Context,
	client rueidis.DedicatedClient,
	m *db.HashMutation,
) (map[string]string, error) {
	if err := checkMutationGuard(ctx, client, m); err != nil {
		return nil, err
	}
	parent, err := client.Do(ctx, client.B().Hgetall().Key(m.Key).Build()).AsStrMap()
	if err != nil {
		return nil, fmt.Errorf("read mutation parent: %w", err)
	}
	return parent, nil
}

func checkMutationGuard(ctx context.Context, client rueidis.DedicatedClient, m *db.HashMutation) error {
	if m.GuardGeneration != "" {
		guard, err := client.Do(ctx, client.B().Hgetall().Key(m.GuardKey).Build()).AsStrMap()
		if err != nil {
			return fmt.Errorf("read collection guard: %w", err)
		}
		if guard["generation"] != m.GuardGeneration || (m.GuardState != "" && guard["state"] != m.GuardState) {
			return domain.ErrNotFound
		}
	}
	return nil
}

func checkMutation(
	m *db.HashMutation, parent map[string]string, markerExpired bool,
) (revision int, exists bool, resultErr error) {
	exists = len(parent) > 0
	revision = 0
	if parent["__revision"] != "" {
		var err error
		revision, err = strconv.Atoi(parent["__revision"])
		if err != nil {
			return 0, exists, fmt.Errorf("invalid stored revision: %w", err)
		}
	}
	version, expired, err := mutationExpiry(m, parent)
	if err != nil {
		return revision, exists, err
	}
	if expired || (markerExpired && m.ExpiredBefore == 0) {
		exists, revision, version = false, 0, ""
	}
	if m.ExpectedVersion != nil &&
		(!exists || *m.ExpectedVersion == "" || version != *m.ExpectedVersion) {
		return revision, exists, fmt.Errorf(
			"check document version: %w",
			domain.NewVersionConflict(version),
		)
	}
	return revision, exists, checkMutationConditions(m, revision, exists)
}

func validateDeadlineMutation(m *db.HashMutation) error {
	if m.DeadlineKey != "" && (m.WorkQueueKey == "" || m.CatalogMember == "" ||
		m.ExpiryMarkerKey == "" || m.ExpiresAt < 0 || m.ExpiresAt > domdoc.MaxExpiresAt) {
		return fmt.Errorf("source deadlines require valid durable document work")
	}
	return nil
}

func mutationExpiry(m *db.HashMutation, parent map[string]string) (version string, expired bool, err error) {
	version = parent["__source_version"]
	if m.DeadlineKey == "" {
		return version, false, nil
	}
	expiresAt, err := domdoc.ParseExpiresAt(parent["__expires_at"])
	if err != nil {
		return version, false, fmt.Errorf("read mutation expiration: %w", err)
	}
	if m.ExpiredBefore > 0 {
		if m.ExpectedVersion == nil || !domdoc.Expired(expiresAt, m.ExpiredBefore) {
			return version, false, fmt.Errorf("source expiration changed: %w", domain.NewVersionConflict(version))
		}
		return version, false, nil
	}
	return version, domdoc.Expired(expiresAt, time.Now().UnixMilli()), nil
}

func checkMutationConditions(m *db.HashMutation, revision int, exists bool) error {
	if m.RequireAbsent && exists {
		return domain.ErrAlreadyExists
	}
	if !matchesExpectedRevision(m.ExpectedRevision, revision, exists) {
		return fmt.Errorf("check document revision: %w", domain.NewRevisionConflict(revision))
	}
	if m.RequireExists && !exists {
		return domain.ErrDocumentNotFound
	}
	if !m.Delete && revision == int(^uint(0)>>1) {
		return fmt.Errorf("revision exhausted")
	}
	return nil
}

func matchesExpectedRevision(expected *int, revision int, exists bool) bool {
	return expected == nil || (exists && *expected == revision)
}

func mutationChildren(parent map[string]string) ([]string, error) {
	if parent["__children"] == "" {
		return nil, nil
	}
	var keys []string
	if err := json.Unmarshal([]byte(parent["__children"]), &keys); err != nil {
		return nil, fmt.Errorf("decode document children: %w", err)
	}
	return keys, nil
}

func replacementFields(
	m *db.HashMutation,
	previous map[string]string,
	revision int,
	mutationID string,
) (map[string]string, error) {
	fields := make(map[string]string, len(previous)+len(m.Set)+2)
	if !m.Replace {
		for key, value := range previous {
			fields[key] = value
		}
	}
	for _, key := range m.Remove {
		delete(fields, key)
	}
	for key, value := range m.Set {
		fields[key] = value
	}
	fields["__revision"] = strconv.Itoa(revision)
	fields["__mutation_id"] = mutationID
	if len(m.Children) > 0 || m.Replace {
		data, err := encodeMutationChildren(m.Children)
		if err != nil {
			return nil, err
		}
		fields["__children"] = string(data)
	}
	return fields, nil
}

func encodeMutationChildren(children []db.HashSetItem) ([]byte, error) {
	keys := make([]string, len(children))
	for i, child := range children {
		keys[i] = child.Key
	}
	data, err := json.Marshal(keys)
	if err != nil {
		return nil, fmt.Errorf("encode document children: %w", err)
	}
	return data, nil
}

func (s *Store) commitHashReplacement(
	ctx context.Context, client rueidis.DedicatedClient, m *db.HashMutation, previous map[string]string,
	oldChildren []string, result db.HashMutationResult,
) (db.HashMutationResult, bool, error) {
	id := rand.Text()
	stagingKey := m.StagingKey
	if stagingKey == "" {
		stagingKey = m.Key + ":pending"
	}
	stagingKey += ":" + id
	fields, err := replacementFields(m, previous, result.Revision, id)
	if err != nil {
		return result, false, err
	}
	if err := s.prepareHashReplacement(ctx, m, stagingKey, fields, id); err != nil {
		s.discardPrepared(m, stagingKey, id)
		return result, false, err
	}
	if err := watchKeys(ctx, client, stagingKey); err != nil {
		s.discardPrepared(m, stagingKey, id)
		return result, false, err
	}
	return s.executeHashReplacement(ctx, client, m, stagingKey, id, oldChildren, result)
}

func (s *Store) executeHashReplacement(
	ctx context.Context, client rueidis.DedicatedClient, m *db.HashMutation, stagingKey, id string,
	oldChildren []string, result db.HashMutationResult,
) (db.HashMutationResult, bool, error) {
	_, committed, err := execTransaction(ctx, client, mutationCommitCommands(client, m, stagingKey, ""))
	if committed {
		if len(m.Children) > 0 || m.Replace {
			s.unlinkHashes(oldChildren)
		}
		return result, true, nil
	}
	// EXEC errors can be ambiguous. Keep prepared children if the parent cannot be
	// read, or if the committed parent references them. Never delete live fragments.
	if err != nil {
		if canRetryTransaction(err) {
			s.discardPrepared(m, stagingKey, id)
			return result, false, err
		}
		return s.recoverHashCommit(client, m, stagingKey, id, oldChildren, result, err)
	}

	s.discardPrepared(m, stagingKey, id)
	return result, false, err
}

func (s *Store) prepareHashReplacement(
	ctx context.Context,
	m *db.HashMutation,
	key string,
	fields map[string]string,
	id string,
) error {
	children, err := s.claimPreparedChildren(ctx, m.Children, id)
	if err != nil {
		return err
	}
	if err := s.HSetMulti(ctx, children); err != nil {
		return fmt.Errorf("prepare fragment hashes: %w", err)
	}
	if err := s.HSet(ctx, key, fields); err != nil {
		return fmt.Errorf("prepare parent hash: %w", err)
	}
	if err := s.Expire(ctx, key, 10*time.Minute, false); err != nil {
		return fmt.Errorf("expire prepared parent: %w", err)
	}
	return nil
}

func (s *Store) claimPreparedChildren(
	ctx context.Context,
	children []db.HashSetItem,
	id string,
) ([]db.HashSetItem, error) {
	if len(children) == 0 {
		return nil, nil
	}
	commands := make([]rueidis.Completed, len(children))
	for i, child := range children {
		commands[i] = s.b().Hsetnx().Key(child.Key).Field("__prepared_by").Value(id).Build()
	}
	replies := s.client.DoMulti(ctx, commands...)
	for _, reply := range replies {
		claimed, err := reply.AsInt64()
		if err != nil {
			return nil, fmt.Errorf("claim prepared fragment: %w", err)
		}
		if claimed != 1 {
			return nil, errPreparedChildBusy
		}
	}
	owned := make([]db.HashSetItem, len(children))
	for i, child := range children {
		fields := make(map[string]string, len(child.Fields)+1)
		for key, value := range child.Fields {
			fields[key] = value
		}
		fields["__prepared_by"] = id
		owned[i] = db.HashSetItem{Key: child.Key, Fields: fields}
	}
	return owned, nil
}

func (s *Store) discardPrepared(m *db.HashMutation, stagingKey, id string) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	parent, err := s.HGetAll(ctx, m.Key)
	if err != nil {
		return
	}
	live, err := mutationChildren(parent)
	if err != nil {
		return
	}
	keep := make(map[string]bool, len(live))
	for _, key := range live {
		keep[key] = true
	}
	obsolete := []string{stagingKey}
	for _, child := range m.Children {
		if keep[child.Key] {
			continue
		}
		owner, ownerErr := s.client.Do(ctx, s.b().Hget().Key(child.Key).Field("__prepared_by").Build()).ToString()
		if ownerErr == nil && owner == id {
			obsolete = append(obsolete, child.Key)
		}
	}
	s.unlinkHashes(obsolete)
}

// Cleanup is deliberately outside EXEC. Each UNLINK is a native command and
// memory reclamation happens asynchronously; no server-side loop walks children.
func (s *Store) unlinkHashes(keys []string) {
	if len(keys) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	for start := 0; start < len(keys); start += 64 {
		end := min(start+64, len(keys))
		commands := make([]rueidis.Completed, 0, end-start)
		for _, key := range keys[start:end] {
			commands = append(commands, s.b().Unlink().Key(key).Build())
		}
		s.client.DoMulti(ctx, commands...)
	}
}

func (s *Store) recoverHashCommit(
	client rueidis.DedicatedClient, m *db.HashMutation, stagingKey, id string, oldChildren []string,
	result db.HashMutationResult, cause error,
) (db.HashMutationResult, bool, error) {
	if !unwatch(client) {
		return result, false, cause
	}

	recoveryCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	parent, readErr := s.HGetAll(recoveryCtx, m.Key)
	cancel()
	if readErr != nil {
		return result, false, cause
	}
	if parent["__mutation_id"] == id {
		if len(m.Children) > 0 || m.Replace {
			s.unlinkHashes(oldChildren)
		}
		return result, true, nil
	}
	s.discardPrepared(m, stagingKey, id)
	return result, false, cause
}
