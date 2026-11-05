package document

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"
	"time"

	"github.com/kailas-cloud/vecdex/internal/db"
	"github.com/kailas-cloud/vecdex/internal/domain"
	domdoc "github.com/kailas-cloud/vecdex/internal/domain/document"
	"github.com/kailas-cloud/vecdex/internal/domain/storage"
)

// List reads a bounded ID window and pipelines only that page's parent hashes.
// Concurrent deletes may shorten a page; its cursor still advances past scanned IDs.
func (r *Repo) List(ctx context.Context, collectionName, cursor string, limit int) ([]domdoc.Document, string, error) {
	if limit <= 0 {
		limit = 20
	}
	if limit == int(^uint(0)>>1) {
		return nil, "", fmt.Errorf("page size overflow: %w", domain.ErrInvalidSchema)
	}
	layout, err := storage.NewCollection(collectionName, domain.CollectionGeneration(ctx))
	if err != nil {
		return nil, "", fmt.Errorf("document layout: %w", err)
	}
	base := layout.Base()
	after, err := decodeDocumentCursor(base, cursor)
	if err != nil {
		return nil, "", err
	}
	ids, err := db.ReadCatalogPage(ctx, r.store, catalogKeys(layout), after, limit+1)
	if err != nil {
		return nil, "", fmt.Errorf("read document IDs: %w", err)
	}
	next := ""
	if len(ids) > limit {
		ids = ids[:limit]
		next = encodeDocumentCursor(base, ids[len(ids)-1])
	}
	if len(ids) == 0 {
		return nil, "", nil
	}
	docs, err := r.readDocumentPage(ctx, layout, ids)
	if err != nil {
		return nil, "", err
	}
	return docs, next, nil
}

func (r *Repo) readDocumentPage(
	ctx context.Context, layout storage.Collection, ids []string,
) ([]domdoc.Document, error) {
	keys := make([]string, len(ids))
	for i, id := range ids {
		keys[i] = layout.Document(id)
	}
	rows, err := r.store.HGetAllMulti(ctx, keys)
	if err != nil {
		return nil, fmt.Errorf("read document page: %w", err)
	}
	if len(rows) != len(ids) {
		return nil, fmt.Errorf("invalid document page response")
	}
	docs := make([]domdoc.Document, 0, len(ids))
	for i, row := range rows {
		if len(row) > 0 {
			doc, err := parseHashFields(ids[i], row)
			if err != nil {
				return nil, err
			}
			if !domdoc.Expired(doc.ExpiresAt(), time.Now().UnixMilli()) {
				docs = append(docs, doc)
			}
		}
	}
	return docs, nil
}

// Count reads the catalog cardinality, without a search-index or keyspace scan.
func (r *Repo) Count(ctx context.Context, collectionName string) (int, error) {
	layout, err := storage.NewCollection(collectionName, domain.CollectionGeneration(ctx))
	if err != nil {
		return 0, fmt.Errorf("document layout: %w", err)
	}
	counts, err := r.liveCounts(ctx, layout)
	if err != nil {
		return 0, fmt.Errorf("count parent documents: %w", err)
	}
	if len(counts) != storage.PartitionCount {
		return 0, fmt.Errorf("partition count response mismatch")
	}
	total := 0
	for _, count := range counts {
		if count < 0 || count > int(^uint(0)>>1)-total {
			return 0, fmt.Errorf("document count overflow")
		}
		total += count
	}
	return total, nil
}

func (r *Repo) liveCounts(ctx context.Context, layout storage.Collection) ([]int, error) {
	backend, ok := r.store.(db.ExpiryStore)
	if !ok {
		counts, err := r.store.CatalogCounts(ctx, catalogKeys(layout))
		if err != nil {
			return nil, fmt.Errorf("read source counts: %w", err)
		}
		return counts, nil
	}
	keys := make([]string, storage.PartitionCount)
	for p := range keys {
		keys[p] = layout.Deadlines(p)
	}
	counts, err := backend.LiveCounts(ctx, keys, time.Now().UnixMilli())
	if err != nil {
		return nil, fmt.Errorf("read live source counts: %w", err)
	}
	return counts, nil
}

func encodeDocumentCursor(base, after string) string {
	return "d3." + base64.RawURLEncoding.EncodeToString([]byte(base+"\x00"+after))
}

func decodeDocumentCursor(base, cursor string) (string, error) {
	if cursor == "" {
		return "", nil
	}
	invalid := func() error { return fmt.Errorf("invalid document cursor: %w", domain.ErrInvalidSchema) }
	if len(cursor) > 1024 || !strings.HasPrefix(cursor, "d3.") {
		return "", invalid()
	}
	decoded, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(cursor, "d3."))
	if err != nil {
		return "", invalid()
	}
	parts := strings.SplitN(string(decoded), "\x00", 2)
	if !validCursorParts(base, parts) {
		return "", invalid()
	}
	return parts[1], nil
}

func validCursorParts(base string, parts []string) bool {
	if len(parts) != 2 || parts[0] != base {
		return false
	}
	return parts[1] != "" && len(parts[1]) <= 256 && !strings.ContainsRune(parts[1], 0)
}

func catalogKeys(layout storage.Collection) []string {
	keys := make([]string, storage.PartitionCount)
	for p := range keys {
		keys[p] = layout.Catalog(p)
	}
	return keys
}
