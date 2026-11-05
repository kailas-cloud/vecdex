package collection

import (
	"context"
	"maps"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/kailas-cloud/vecdex/internal/db"
	"github.com/kailas-cloud/vecdex/internal/domain"
	domcol "github.com/kailas-cloud/vecdex/internal/domain/collection"
	"github.com/kailas-cloud/vecdex/internal/domain/collection/field"
)

const testVectorDim = 1024

type mockStore struct {
	mu                 sync.Mutex
	rows               map[string]map[string]string
	catalogs           map[string]map[string]bool
	indexes            map[string]*db.IndexDefinition
	expiry             map[string]time.Duration
	mutateFn           func(context.Context, *db.HashMutation) (db.HashMutationResult, error)
	createIndexFn      func(context.Context, *db.IndexDefinition) error
	dropIndexFn        func(context.Context, string) error
	waitForIndexFn     func(context.Context, string) error
	supportsTextSearch bool
}

func newTestRepo(t *testing.T) (*Repo, *mockStore) {
	t.Helper()
	s := &mockStore{rows: map[string]map[string]string{}, catalogs: map[string]map[string]bool{},
		indexes: map[string]*db.IndexDefinition{}, expiry: map[string]time.Duration{}}
	return New(s, testVectorDim), s
}

func (s *mockStore) HGetAll(ctx context.Context, key string) (map[string]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return maps.Clone(s.rows[key]), nil
}
func (s *mockStore) HGetAllMulti(ctx context.Context, keys []string) ([]map[string]string, error) {
	out := make([]map[string]string, len(keys))
	for i, key := range keys {
		row, err := s.HGetAll(ctx, key)
		if err != nil {
			return nil, err
		}
		out[i] = row
	}
	return out, nil
}
func (s *mockStore) MutateHash(ctx context.Context, m *db.HashMutation) (db.HashMutationResult, error) {
	if err := ctx.Err(); err != nil {
		return db.HashMutationResult{}, err
	}
	if s.mutateFn != nil {
		return s.mutateFn(ctx, m)
	}
	return s.applyMutation(ctx, m)
}
func (s *mockStore) applyMutation(_ context.Context, m *db.HashMutation) (db.HashMutationResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current := s.rows[m.Key]
	revision, _ := strconv.Atoi(current["__revision"])
	if m.RequireAbsent && current != nil {
		return db.HashMutationResult{}, domain.ErrAlreadyExists
	}
	if m.RequireExists && current == nil {
		return db.HashMutationResult{}, domain.ErrDocumentNotFound
	}
	if m.ExpectedRevision != nil && *m.ExpectedRevision != revision {
		return db.HashMutationResult{}, domain.NewRevisionConflict(revision)
	}
	if err := s.checkGuard(m); err != nil {
		return db.HashMutationResult{}, err
	}
	result := db.HashMutationResult{Created: current == nil, Revision: revision + 1}
	if m.Delete {
		delete(s.rows, m.Key)
	} else {
		row := maps.Clone(current)
		if row == nil || m.Replace {
			row = map[string]string{}
		}
		for key, value := range m.Set {
			row[key] = value
		}
		row["__revision"] = strconv.Itoa(result.Revision)
		s.rows[m.Key] = row
	}
	if m.CatalogKey != "" {
		s.member(m.CatalogKey, m.CatalogMember, !m.Delete)
	}
	if m.LifecycleKey != "" {
		s.member(m.LifecycleKey, m.LifecycleMember, m.LifecyclePending)
	}
	return result, nil
}
func (s *mockStore) member(key, id string, present bool) {
	if s.catalogs[key] == nil {
		s.catalogs[key] = map[string]bool{}
	}
	if present {
		s.catalogs[key][id] = true
	} else {
		delete(s.catalogs[key], id)
	}
}
func (s *mockStore) CatalogIDs(ctx context.Context, key, after string, limit int) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []string{}
	for id := range s.catalogs[key] {
		if id > after {
			out = append(out, id)
		}
	}
	slices.Sort(out)
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}
func (s *mockStore) CatalogIDPages(ctx context.Context, keys []string, after string, limit int) ([][]string, error) {
	out := make([][]string, len(keys))
	for i, key := range keys {
		page, err := s.CatalogIDs(ctx, key, after, limit)
		if err != nil {
			return nil, err
		}
		out[i] = page
	}
	return out, nil
}
func (s *mockStore) CatalogCount(_ context.Context, key string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.catalogs[key]), nil
}
func (s *mockStore) CatalogCounts(ctx context.Context, keys []string) ([]int, error) {
	out := make([]int, len(keys))
	for i, key := range keys {
		out[i], _ = s.CatalogCount(ctx, key)
	}
	return out, nil
}
func (*mockStore) RecoverMutations(context.Context, string, int) (int, error) { return 0, nil }
func (*mockStore) CancelMutations(context.Context, string, int) (int, error)  { return 0, nil }
func (s *mockStore) Expire(_ context.Context, key string, duration time.Duration, _ bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expiry[key] = duration
	return nil
}
func (s *mockStore) CreateIndex(ctx context.Context, index *db.IndexDefinition) error {
	if s.createIndexFn != nil {
		if err := s.createIndexFn(ctx, index); err != nil {
			return err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.indexes[index.Name] != nil {
		return db.ErrIndexExists
	}
	s.indexes[index.Name] = index
	return nil
}
func (s *mockStore) DropIndex(ctx context.Context, name string) error {
	if s.dropIndexFn != nil {
		if err := s.dropIndexFn(ctx, name); err != nil {
			return err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.indexes, name)
	return nil
}
func (s *mockStore) IndexExists(_ context.Context, name string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.indexes[name] != nil, nil
}
func (s *mockStore) SupportsTextSearch(context.Context) bool { return s.supportsTextSearch }
func testCollection(t *testing.T) domcol.Collection {
	t.Helper()
	return domcol.Reconstruct("test-collection", domcol.TypeText, []field.Field{
		field.Reconstruct("language", field.Tag), field.Reconstruct("priority", field.Numeric),
	}, 1024, 1700000000000, 1).WithGeneration("fixture")
}

func (s *mockStore) checkGuard(m *db.HashMutation) error {
	if m.GuardGeneration != "" {
		guard := s.rows[m.GuardKey]
		if guard["generation"] != m.GuardGeneration || m.GuardState != "" && guard["state"] != m.GuardState {
			return domain.ErrNotFound
		}
	}
	return nil
}

func (s *mockStore) WaitForIndex(ctx context.Context, name string) error {
	if s.waitForIndexFn != nil {
		return s.waitForIndexFn(ctx, name)
	}
	return ctx.Err()
}
