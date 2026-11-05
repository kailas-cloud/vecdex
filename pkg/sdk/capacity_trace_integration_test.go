package vecdex

import (
	"context"
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kailas-cloud/vecdex/internal/db"
)

// The observer copies successful store replies before the repository filters
// uncommitted fragments. It distinguishes engine omission from SDK projection.
type capacityTraceStore struct {
	db.Store
	mu   sync.Mutex
	last capacitySearchTrace
}

type capacitySearchTrace struct {
	Time           time.Time           `json:"time"`
	Mode           string              `json:"mode"`
	Index          string              `json:"index"`
	Vector         []float32           `json:"vector,omitempty"`
	StoreReply     *capacityStoreReply `json:"store_reply"`
	ParentKeys     []string            `json:"parent_keys"`
	ParentVersions []string            `json:"parent_versions"`
}

type capacityStoreReply struct {
	Total   int                  `json:"total"`
	Entries []capacityStoreEntry `json:"entries"`
}

type capacityStoreEntry struct {
	Key    string            `json:"key"`
	Score  float64           `json:"score"`
	Fields map[string]string `json:"fields"`
}

func (s *capacityTraceStore) SearchKNN(ctx context.Context, q *db.KNNQuery) (*db.SearchResult, error) {
	reply, err := s.Store.SearchKNN(ctx, q)
	if err == nil {
		s.record("semantic", q.IndexName, q.Vector, reply)
	}
	return reply, err
}

func (s *capacityTraceStore) SearchBM25(ctx context.Context, q *db.TextQuery) (*db.SearchResult, error) {
	reply, err := s.Store.SearchBM25(ctx, q)
	if err == nil {
		s.record("keyword", q.IndexName, nil, reply)
	}
	return reply, err
}

func (s *capacityTraceStore) record(mode, index string, vector []float32, reply *db.SearchResult) {
	if reply == nil {
		return
	}
	copyReply := &capacityStoreReply{Total: reply.Total, Entries: make([]capacityStoreEntry, len(reply.Entries))}
	for i, entry := range reply.Entries {
		copyReply.Entries[i] = capacityStoreEntry{Key: entry.Key, Score: entry.Score, Fields: maps.Clone(entry.Fields)}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.last = capacitySearchTrace{
		Time: time.Now().UTC(), Mode: mode, Index: index, Vector: slices.Clone(vector), StoreReply: copyReply,
	}
}

func (s *capacityTraceStore) HGetFieldsMulti(ctx context.Context, keys, fields []string) ([][]string, error) {
	rows, err := s.Store.HGetFieldsMulti(ctx, keys, fields)
	if err == nil && len(fields) > 0 && fields[0] == "__chunk_version" {
		s.mu.Lock()
		s.last.ParentKeys = slices.Clone(keys)
		s.last.ParentVersions = make([]string, len(rows))
		for i, row := range rows {
			if len(row) > 0 {
				s.last.ParentVersions[i] = row[0]
			}
		}
		s.mu.Unlock()
	}
	return rows, err
}

func (s *capacityTraceStore) report(t *testing.T) {
	t.Helper()
	s.mu.Lock()
	data, err := json.MarshalIndent(s.last, "", "  ")
	s.mu.Unlock()
	if err != nil {
		t.Error(err)
		return
	}
	directory := filepath.Join("..", "..", "tests", "reports")
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Error(err)
		return
	}
	path := filepath.Join(directory, "capacity-search-failure.json")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Error(err)
	}
	t.Logf("search failure before source/version filtering: %s", data)
}

func newTracedCapacityClient(t *testing.T, ctx context.Context, seeds string) (*Client, *capacityTraceStore) {
	t.Helper()
	cfg := &clientConfig{vectorDimensions: capacityDimensions, embedder: capacityEmbedder{}}
	WithValkeyAddresses(strings.Split(seeds, ","), "").apply(cfg)
	store, err := createStore(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.WaitForReady(ctx, defaultReadinessTimeout); err != nil {
		store.Close()
		t.Fatal(err)
	}
	observer, err := newObserver(nil, nil)
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	traced := &capacityTraceStore{Store: store}
	client, err := wireClient(traced, cfg, observer)
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	return client, traced
}
