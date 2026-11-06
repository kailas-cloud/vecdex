package collection

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"

	"github.com/kailas-cloud/vecdex/internal/db"
	"github.com/kailas-cloud/vecdex/internal/domain"
	domcol "github.com/kailas-cloud/vecdex/internal/domain/collection"
	"github.com/kailas-cloud/vecdex/internal/domain/storage"
)

// Inspect reads collection metadata without hiding recoverable lifecycle work.
func (r *Repo) Inspect(ctx context.Context, name string) (domcol.Inspection, error) {
	if _, err := storage.NewCollection(name, "validation"); err != nil {
		return domcol.Inspection{}, fmt.Errorf("invalid collection name: %w", domain.ErrInvalidSchema)
	}
	row, err := r.store.HGetAll(ctx, storage.Metadata(name))
	if err != nil {
		return domcol.Inspection{}, fmt.Errorf("inspect collection: %w", err)
	}
	if len(row) == 0 {
		return domcol.Inspection{Name: name, State: "absent"}, nil
	}
	col, err := collectionFromHash(row, r.defaultVectorDim)
	if err != nil {
		return domcol.Inspection{}, err
	}
	return domcol.Inspection{Name: name, Generation: col.Generation(), State: row["state"], Collection: &col}, nil
}

// ListPage scans only one bounded catalog window, not every collection.
func (r *Repo) ListPage(ctx context.Context, cursor string, limit int) ([]domcol.Collection, string, error) {
	if limit < 1 || limit > 100 {
		return nil, "", fmt.Errorf("invalid collection page size: %w", domain.ErrInvalidSchema)
	}
	after, err := decodeCollectionCursor(cursor)
	if err != nil {
		return nil, "", err
	}
	keys := make([]string, storage.ControlPartitionCount)
	for p := range keys {
		keys[p] = storage.CollectionCatalog(p)
	}
	names, err := db.ReadCatalogPage(ctx, r.store, keys, after, limit+1)
	if err != nil {
		return nil, "", fmt.Errorf("read collection catalog page: %w", err)
	}
	next := ""
	if len(names) > limit {
		names = names[:limit]
		next = "c1." + base64.RawURLEncoding.EncodeToString([]byte(names[len(names)-1]))
	}
	if len(names) == 0 {
		return []domcol.Collection{}, next, nil
	}
	metadataKeys := make([]string, len(names))
	for i, name := range names {
		metadataKeys[i] = storage.Metadata(name)
	}
	rows, err := r.store.HGetAllMulti(ctx, metadataKeys)
	if err != nil {
		return nil, "", fmt.Errorf("read collection page: %w", err)
	}
	if len(rows) != len(names) {
		return nil, "", fmt.Errorf("collection page response size mismatch")
	}
	collections := make([]domcol.Collection, 0, len(rows))
	for _, row := range rows {
		if row["state"] != "active" {
			continue
		}
		col, err := collectionFromHash(row, r.defaultVectorDim)
		if err != nil {
			return nil, "", err
		}
		collections = append(collections, col)
	}
	return collections, next, nil
}

func decodeCollectionCursor(cursor string) (string, error) {
	if cursor == "" {
		return "", nil
	}
	invalid := func() error { return fmt.Errorf("invalid collection cursor: %w", domain.ErrInvalidSchema) }
	if len(cursor) > 128 || !strings.HasPrefix(cursor, "c1.") {
		return "", invalid()
	}
	decoded, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(cursor, "c1."))
	if err != nil {
		return "", invalid()
	}
	if _, err := storage.NewCollection(string(decoded), "validation"); err != nil {
		return "", invalid()
	}
	return string(decoded), nil
}
