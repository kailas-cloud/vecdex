package document

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/kailas-cloud/vecdex/internal/db"
	"github.com/kailas-cloud/vecdex/internal/domain"
	domdoc "github.com/kailas-cloud/vecdex/internal/domain/document"
)

type sourceProjectionStore struct {
	*mockStore
	project func([]string, []string) ([][]string, error)
}

func (s *sourceProjectionStore) HGetFieldsMulti(_ context.Context, keys, fields []string) ([][]string, error) {
	return s.project(keys, fields)
}

func TestMetadataProjectionAvoidsTextAndFragmentReads(t *testing.T) {
	store := &sourceProjectionStore{mockStore: &mockStore{
		hgetAllFn: func(context.Context, string) (map[string]string, error) {
			t.Fatal("projection hydrated parent")
			return nil, errors.New("unexpected hydration")
		},
		hgetFieldMultiFn: func(context.Context, []string, string) ([]string, error) {
			t.Fatal("projection hydrated vectors")
			return nil, errors.New("unexpected hydration")
		},
	}}
	store.project = func(keys, fields []string) ([][]string, error) {
		if len(keys) != 1 || slices.Contains(fields, "__content") ||
			slices.Contains(fields, "__parts") || slices.Contains(fields, "__vector") {
			t.Fatalf("unexpected projection: %v", fields)
		}
		values := map[string]string{
			"__source_version": "version", "__expires_at": "0", "__revision": "2",
			"__source_metadata": `{"nested":{"n":9007199254740993}}`,
		}
		row := make([]string, len(fields))
		for i, field := range fields {
			row[i] = values[field]
		}
		return [][]string{row}, nil
	}
	doc, err := New(store).ReadSource(testContext(), "notes", "source", domdoc.SourceProjection{Metadata: true})
	if err != nil || doc.Version() != "version" || doc.Content() != "" || len(doc.Vector()) != 0 ||
		string(doc.Metadata().JSON()) != `{"nested":{"n":9007199254740993}}` {
		t.Fatalf("projection lost source data: %v %v", doc, err)
	}
}

func TestSourceProjectionHidesExpiredParents(t *testing.T) {
	store := &sourceProjectionStore{mockStore: &mockStore{},
		project: func(_ []string, fields []string) ([][]string, error) {
			row := make([]string, len(fields))
			row[0] = "version"
			row[1] = strconv.FormatInt(time.Now().Add(-time.Second).UnixMilli(), 10)
			row[2] = "1"
			return [][]string{row}, nil
		}}
	_, err := New(store).ReadSource(testContext(), "notes", "source", domdoc.SourceProjection{})
	if !errors.Is(err, domain.ErrDocumentNotFound) {
		t.Fatalf("expired source leaked: %v", err)
	}
}

func TestSourcePageKeepsCursorWhenParentsDisappear(t *testing.T) {
	store := &sourceProjectionStore{mockStore: &mockStore{
		catalogIDsFn: func(context.Context, string, string, int) ([]string, error) {
			return []string{"gone", "next"}, nil
		},
	},
		project: func(_ []string, fields []string) ([][]string, error) {
			return [][]string{make([]string, len(fields))}, nil
		}}
	docs, cursor, err := New(store).ListSources(testContext(), "notes", "", 1, domdoc.SourceProjection{})
	if err != nil || len(docs) != 0 || cursor == "" {
		t.Fatalf("empty live page lost continuation: %v %q %v", docs, cursor, err)
	}
}

func TestCreateOnlyReachesAtomicMutation(t *testing.T) {
	repo, store := newTestRepo(t)
	store.mutateFn = func(_ context.Context, m *db.HashMutation) (db.HashMutationResult, error) {
		if !m.RequireAbsent {
			t.Fatal("create-only condition was lost")
		}
		return db.HashMutationResult{}, domain.ErrAlreadyExists
	}
	doc := testDocument(t)
	_, err := repo.Upsert(domain.WithRequireAbsent(testContext(), true), "notes", &doc)
	if !errors.Is(err, domain.ErrAlreadyExists) {
		t.Fatalf("existing memory was overwritten: %v", err)
	}
}
