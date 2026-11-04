package document

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"

	"github.com/kailas-cloud/vecdex/internal/domain"
	domdoc "github.com/kailas-cloud/vecdex/internal/domain/document"
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
	base := domain.CollectionDataBase(collectionName, domain.CollectionGeneration(ctx))
	after, err := decodeDocumentCursor(base, cursor)
	if err != nil {
		return nil, "", err
	}
	ids, err := r.store.CatalogIDs(
		ctx,
		domain.DocumentCatalogKey(collectionName, domain.CollectionGeneration(ctx)),
		after,
		limit+1,
	)
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
	keys := make([]string, len(ids))
	for i, id := range ids {
		keys[i] = documentKey(ctx, collectionName, id)
	}
	rows, err := r.store.HGetAllMulti(ctx, keys)
	if err != nil {
		return nil, "", fmt.Errorf("read document page: %w", err)
	}
	if len(rows) != len(ids) {
		return nil, "", fmt.Errorf("invalid document page response")
	}
	docs := make([]domdoc.Document, 0, len(ids))
	for i, row := range rows {
		if len(row) > 0 {
			docs = append(docs, parseHashFields(ids[i], row))
		}
	}
	return docs, next, nil
}

// Count reads the catalog cardinality, without a search-index or keyspace scan.
func (r *Repo) Count(ctx context.Context, collectionName string) (int, error) {
	count, err := r.store.CatalogCount(ctx, domain.DocumentCatalogKey(collectionName, domain.CollectionGeneration(ctx)))
	if err != nil {
		return 0, fmt.Errorf("count parent documents: %w", err)
	}
	return count, nil
}

func encodeDocumentCursor(base, after string) string {
	return "d1." + base64.RawURLEncoding.EncodeToString([]byte(base+"\x00"+after))
}

func decodeDocumentCursor(base, cursor string) (string, error) {
	if cursor == "" {
		return "", nil
	}
	invalid := func() error { return fmt.Errorf("invalid document cursor: %w", domain.ErrInvalidSchema) }
	if len(cursor) > 1024 || !strings.HasPrefix(cursor, "d1.") {
		return "", invalid()
	}
	decoded, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(cursor, "d1."))
	if err != nil {
		return "", invalid()
	}
	parts := strings.SplitN(string(decoded), "\x00", 2)
	if len(parts) != 2 || parts[0] != base || parts[1] == "" || len(parts[1]) > 256 ||
		strings.ContainsRune(parts[1], 0) {
		return "", invalid()
	}
	return parts[1], nil
}
