package document

import (
	"context"
	"encoding/json"
	"errors"
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
	JSONSet(ctx context.Context, key, path string, data []byte) error
	JSONGet(ctx context.Context, key string, paths ...string) ([]byte, error)
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
	jsonDoc := buildJSONDoc(doc)
	data, err := json.Marshal(jsonDoc)
	if err != nil {
		return false, fmt.Errorf("marshal document: %w", err)
	}

	exists, err := r.store.Exists(ctx, key)
	if err != nil {
		return false, fmt.Errorf("check exists %s: %w", key, err)
	}

	if err := r.store.JSONSet(ctx, key, "$", data); err != nil {
		return false, fmt.Errorf("json.set %s: %w", key, err)
	}

	return !exists, nil
}

// Get returns a document by ID.
func (r *Repo) Get(ctx context.Context, collectionName, id string) (domdoc.Document, error) {
	key := docKey(collectionName, id)
	raw, err := r.store.JSONGet(ctx, key, "$")
	if err != nil {
		if errors.Is(err, db.ErrKeyNotFound) {
			return domdoc.Document{}, domain.ErrDocumentNotFound
		}
		return domdoc.Document{}, fmt.Errorf("json.get %s: %w", key, err)
	}
	return parseJSONGetResult(id, string(raw))
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

	result, err := r.store.SearchList(ctx, idxName, "*", offset, fetchCount, []string{"$"})
	if err != nil {
		return nil, "", fmt.Errorf("search list %s: %w", collectionName, err)
	}

	if result == nil || result.Total == 0 {
		return nil, "", nil
	}

	docs := make([]domdoc.Document, 0, limit)
	for i, entry := range result.Entries {
		if i >= limit {
			break
		}
		docID := extractDocID(entry.Key, collectionName)
		jsonStr := entry.Fields["$"]
		if jsonStr == "" {
			docs = append(docs, domdoc.Reconstruct(docID, "", nil, nil, nil, 0))
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(jsonStr), &m); err != nil {
			docs = append(docs, domdoc.Reconstruct(docID, "", nil, nil, nil, 0))
			continue
		}
		docs = append(docs, parseDocMap(docID, m))
	}

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

// Patch performs a partial update: JSON.GET, merge fields, JSON.SET.
func (r *Repo) Patch(ctx context.Context, collectionName, id string, p patch.Patch, newVector []float32) error {
	key := docKey(collectionName, id)

	raw, err := r.store.JSONGet(ctx, key, "$")
	if err != nil {
		if errors.Is(err, db.ErrKeyNotFound) {
			return domain.ErrDocumentNotFound
		}
		return fmt.Errorf("json.get %s: %w", key, err)
	}

	var docs []map[string]any
	if err := json.Unmarshal(raw, &docs); err != nil {
		return fmt.Errorf("unmarshal for patch: %w", err)
	}
	if len(docs) == 0 {
		return domain.ErrDocumentNotFound
	}

	current := docs[0]
	applyPatchFields(current, p, newVector)

	data, err := json.Marshal(current)
	if err != nil {
		return fmt.Errorf("marshal patched doc: %w", err)
	}

	if err := r.store.JSONSet(ctx, key, "$", data); err != nil {
		return fmt.Errorf("json.set %s: %w", key, err)
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

// applyPatchFields merges patch fields into the current JSON map in-place.
func applyPatchFields(current map[string]any, p patch.Patch, newVector []float32) {
	if p.HasContent() {
		current["__content"] = *p.Content()
	}
	for k, v := range p.Tags() {
		if v == nil {
			delete(current, k)
		} else {
			current[k] = *v
		}
	}
	for k, v := range p.Numerics() {
		if v == nil {
			delete(current, k)
		} else {
			current[k] = *v
		}
	}
	if newVector != nil {
		current["__vector"] = newVector
	}
}
