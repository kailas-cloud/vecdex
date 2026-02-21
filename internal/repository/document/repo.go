package document

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/kailas-cloud/vecdex/internal/db"
	"github.com/kailas-cloud/vecdex/internal/domain"
	domdoc "github.com/kailas-cloud/vecdex/internal/domain/document"
	"github.com/kailas-cloud/vecdex/internal/domain/document/patch"
)

// store is the consumer interface for documents (ISP).
type store interface {
	HSet(ctx context.Context, key string, fields map[string]string) error
	HSetMulti(ctx context.Context, items []db.HashSetItem) error
	HGetAll(ctx context.Context, key string) (map[string]string, error)
	Del(ctx context.Context, key string) error
	Exists(ctx context.Context, key string) (bool, error)
	SearchList(ctx context.Context, index, query string, offset, limit int, fields []string) (*db.SearchResult, error)
	SearchCount(ctx context.Context, index, query string) (int, error)
}

// Repo implements usecase/document.Repository.
type Repo struct {
	store store
}

// New creates a document repository.
func New(s store) *Repo {
	return &Repo{store: s}
}

// Upsert creates or updates a document. Returns true if created.
func (r *Repo) Upsert(ctx context.Context, collectionName string, doc *domdoc.Document) (bool, error) {
	key := docKey(collectionName, doc.ID())
	fields := buildHashFields(doc)

	exists, err := r.store.Exists(ctx, key)
	if err != nil {
		return false, fmt.Errorf("check exists %s: %w", key, err)
	}

	if err := r.store.HSet(ctx, key, fields); err != nil {
		return false, fmt.Errorf("hset %s: %w", key, err)
	}

	return !exists, nil
}

// BatchUpsert stores multiple documents in a single pipelined round-trip.
// Skips per-key existence checks — designed for bulk load.
func (r *Repo) BatchUpsert(ctx context.Context, collectionName string, docs []domdoc.Document) error {
	if len(docs) == 0 {
		return nil
	}

	items := make([]db.HashSetItem, len(docs))
	for i := range docs {
		items[i] = db.HashSetItem{
			Key:    docKey(collectionName, docs[i].ID()),
			Fields: buildHashFields(&docs[i]),
		}
	}

	if err := r.store.HSetMulti(ctx, items); err != nil {
		return fmt.Errorf("hset multi: %w", err)
	}
	return nil
}

// Get returns a document by ID.
func (r *Repo) Get(ctx context.Context, collectionName, id string) (domdoc.Document, error) {
	key := docKey(collectionName, id)
	m, err := r.store.HGetAll(ctx, key)
	if err != nil {
		return domdoc.Document{}, fmt.Errorf("hgetall %s: %w", key, err)
	}
	if len(m) == 0 {
		return domdoc.Document{}, domain.ErrDocumentNotFound
	}
	return parseHashFields(id, m), nil
}

// List returns documents with cursor-based pagination via FT.SEARCH.
func (r *Repo) List(ctx context.Context, collectionName, cursor string, limit int) (
	[]domdoc.Document, string, error,
) {
	if limit <= 0 {
		limit = 20
	}

	offset := 0
	if cursor != "" {
		parsed, err := strconv.Atoi(cursor)
		if err != nil {
			return nil, "", fmt.Errorf("invalid cursor %q: %w: %w", cursor, domain.ErrInvalidSchema, err)
		}
		offset = parsed
	}

	idxName := indexName(collectionName)
	fetchCount := limit + 1

	result, err := r.store.SearchList(ctx, idxName, "*", offset, fetchCount, nil)
	if err != nil {
		return nil, "", fmt.Errorf("search list %s: %w", collectionName, err)
	}

	if result == nil || result.Total == 0 {
		return nil, "", nil
	}

	docs := parseListEntries(result.Entries, collectionName, limit)

	var nextCursor string
	if len(result.Entries) > limit {
		nextCursor = strconv.Itoa(offset + limit)
	}

	return docs, nextCursor, nil
}

// Count returns the number of documents in a collection.
func (r *Repo) Count(ctx context.Context, collectionName string) (int, error) {
	n, err := r.store.SearchCount(ctx, indexName(collectionName), "*")
	if err != nil {
		return 0, fmt.Errorf("search count %s: %w", collectionName, err)
	}
	return n, nil
}

// Delete removes a document.
func (r *Repo) Delete(ctx context.Context, collectionName, id string) error {
	key := docKey(collectionName, id)

	exists, err := r.store.Exists(ctx, key)
	if err != nil {
		return fmt.Errorf("check exists %s: %w", key, err)
	}
	if !exists {
		return domain.ErrDocumentNotFound
	}

	if err := r.store.Del(ctx, key); err != nil {
		return fmt.Errorf("del %s: %w", key, err)
	}
	return nil
}

// Patch performs a partial update: HGetAll, merge fields, HSet.
func (r *Repo) Patch(ctx context.Context, collectionName, id string, p patch.Patch, newVector []float32) error {
	key := docKey(collectionName, id)

	m, err := r.store.HGetAll(ctx, key)
	if err != nil {
		return fmt.Errorf("hgetall %s: %w", key, err)
	}
	if len(m) == 0 {
		return domain.ErrDocumentNotFound
	}

	applyPatchToHash(m, p, newVector)

	if err := r.store.HSet(ctx, key, m); err != nil {
		return fmt.Errorf("hset %s: %w", key, err)
	}
	return nil
}

func docKey(collection, id string) string {
	return fmt.Sprintf("%s%s:%s", domain.KeyPrefix, collection, id)
}

func indexName(collection string) string {
	return fmt.Sprintf("%s%s:idx", domain.KeyPrefix, collection)
}

func extractDocID(key, collection string) string {
	prefix := fmt.Sprintf("%s%s:", domain.KeyPrefix, collection)
	return strings.TrimPrefix(key, prefix)
}

// parseListEntries converts search entries into domain documents, capping at limit.
func parseListEntries(entries []db.SearchEntry, collectionName string, limit int) []domdoc.Document {
	docs := make([]domdoc.Document, 0, limit)
	for i, entry := range entries {
		if i >= limit {
			break
		}
		docID := extractDocID(entry.Key, collectionName)
		docs = append(docs, parseHashFields(docID, entry.Fields))
	}
	return docs
}

// applyPatchToHash merges patch fields into the current hash map in-place.
func applyPatchToHash(m map[string]string, p patch.Patch, newVector []float32) {
	if p.HasContent() {
		m["__content"] = *p.Content()
	}
	for k, v := range p.Tags() {
		if v == nil {
			delete(m, k)
		} else {
			m[k] = *v
		}
	}
	for k, v := range p.Numerics() {
		if v == nil {
			delete(m, k)
		} else {
			m[k] = strconv.FormatFloat(*v, 'f', -1, 64)
		}
	}
	if newVector != nil {
		m["__vector"] = vectorToBytes(newVector)
	}
}
