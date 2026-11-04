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
	"github.com/redis/rueidis"
)

var errPreparedChildBusy = errors.New("fragment key is already owned by another write")

// MutateHash prepares immutable hashes outside the transaction. WATCH protects
// the parent and collection generation; the commit uses RENAME and PERSIST.
func (s *Store) MutateHash(ctx context.Context, m *db.HashMutation) (db.HashMutationResult, error) {
	if (m.CatalogKey == "") != (m.CatalogMember == "") {
		return db.HashMutationResult{}, fmt.Errorf("catalog key and member must be supplied together")
	}
	for attempt := 0; attempt < maxTransactionAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return db.HashMutationResult{}, fmt.Errorf("document write canceled: %w", err)
		}
		result, committed, err := s.mutateHashAttempt(ctx, m)
		if errors.Is(err, errPreparedChildBusy) {
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
	keys := []string{m.Key}
	if m.GuardGeneration != "" {
		keys = append(keys, m.GuardKey)
	}
	if err := watchKeys(ctx, client, keys...); err != nil {
		return db.HashMutationResult{}, false, err
	}
	if err := checkCatalogType(ctx, client, m.CatalogKey); err != nil {
		return db.HashMutationResult{}, false, err
	}
	previous, err := readMutationParent(ctx, client, m)
	if err != nil {
		return db.HashMutationResult{}, false, err
	}
	revision, exists, err := checkMutation(m, previous)
	if err != nil {
		return db.HashMutationResult{}, false, err
	}
	oldChildren, err := mutationChildren(previous)
	if err != nil {
		return db.HashMutationResult{}, false, err
	}
	result := db.HashMutationResult{Revision: revision, Created: !exists}
	if m.Delete {
		_, committed, err := execTransaction(ctx, client, mutationCommitCommands(client, m, ""))
		if committed {
			s.unlinkHashes(oldChildren)
		}
		return result, committed, err
	}
	result.Revision++
	return s.commitHashReplacement(ctx, client, m, previous, oldChildren, result)
}

func readMutationParent(
	ctx context.Context,
	client rueidis.DedicatedClient,
	m *db.HashMutation,
) (map[string]string, error) {
	if m.GuardGeneration != "" {
		guard, err := client.Do(ctx, client.B().Hgetall().Key(m.GuardKey).Build()).AsStrMap()
		if err != nil {
			return nil, fmt.Errorf("read collection guard: %w", err)
		}
		if guard["generation"] != m.GuardGeneration || (m.GuardState != "" && guard["state"] != m.GuardState) {
			return nil, domain.ErrNotFound
		}
	}
	parent, err := client.Do(ctx, client.B().Hgetall().Key(m.Key).Build()).AsStrMap()
	if err != nil {
		return nil, fmt.Errorf("read mutation parent: %w", err)
	}
	return parent, nil
}

func checkMutation(m *db.HashMutation, parent map[string]string) (revision int, exists bool, resultErr error) {
	exists = len(parent) > 0
	revision = 0
	if parent["__revision"] != "" {
		var err error
		revision, err = strconv.Atoi(parent["__revision"])
		if err != nil {
			return 0, exists, fmt.Errorf("invalid stored revision: %w", err)
		}
	}
	if m.RequireAbsent && exists {
		return 0, exists, domain.ErrAlreadyExists
	}
	if m.ExpectedRevision != nil && (!exists || *m.ExpectedRevision != revision) {
		return revision, exists, fmt.Errorf("check document revision: %w", domain.NewRevisionConflict(revision))
	}
	if m.RequireExists && !exists {
		return 0, false, domain.ErrDocumentNotFound
	}
	if !m.Delete && revision == int(^uint(0)>>1) {
		return 0, exists, fmt.Errorf("revision exhausted")
	}
	return revision, exists, nil
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
		keys := make([]string, len(m.Children))
		for i, child := range m.Children {
			keys[i] = child.Key
		}
		data, err := json.Marshal(keys)
		if err != nil {
			return nil, fmt.Errorf("encode document children: %w", err)
		}
		fields["__children"] = string(data)
	}
	return fields, nil
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
	_, committed, err := execTransaction(ctx, client, mutationCommitCommands(client, m, stagingKey))
	if committed {
		if len(m.Children) > 0 || m.Replace {
			s.unlinkHashes(oldChildren)
		}
		return result, true, nil
	}
	// EXEC errors can be ambiguous. Keep prepared children if the parent cannot be
	// read, or if the committed parent references them. Never delete live fragments.
	if err != nil {
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
