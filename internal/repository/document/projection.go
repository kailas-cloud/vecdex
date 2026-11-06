package document

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/kailas-cloud/vecdex/internal/db"
	"github.com/kailas-cloud/vecdex/internal/domain"
	domdoc "github.com/kailas-cloud/vecdex/internal/domain/document"
	"github.com/kailas-cloud/vecdex/internal/domain/storage"
)

type projectionStore interface {
	HGetFieldsMulti(context.Context, []string, []string) ([][]string, error)
}

// ReadSource reads only selected parent fields and never hydrates fragment vectors.
func (r *Repo) ReadSource(
	ctx context.Context, collection, id string, projection domdoc.SourceProjection,
) (domdoc.Document, error) {
	layout, err := storage.NewCollection(collection, domain.CollectionGeneration(ctx))
	if err != nil {
		return domdoc.Document{}, fmt.Errorf("source layout: %w", err)
	}
	docs, err := r.readSourcePage(ctx, layout, []string{id}, projection)
	if err != nil {
		return domdoc.Document{}, err
	}
	if len(docs) == 0 {
		return domdoc.Document{}, domain.ErrDocumentNotFound
	}
	return docs[0], nil
}

// ListSources combines a bounded catalog window with parent-only projection reads.
func (r *Repo) ListSources(
	ctx context.Context, collection, cursor string, limit int, projection domdoc.SourceProjection,
) ([]domdoc.Document, string, error) {
	if limit < 1 || limit > 100 {
		return nil, "", fmt.Errorf("invalid source page size: %w", domain.ErrInvalidSchema)
	}
	layout, err := storage.NewCollection(collection, domain.CollectionGeneration(ctx))
	if err != nil {
		return nil, "", fmt.Errorf("source layout: %w", err)
	}
	after, err := decodeDocumentCursor(layout.Base(), cursor)
	if err != nil {
		return nil, "", err
	}
	ids, err := db.ReadCatalogPage(ctx, r.store, catalogKeys(layout), after, limit+1)
	if err != nil {
		return nil, "", fmt.Errorf("read source IDs: %w", err)
	}
	next := ""
	if len(ids) > limit {
		ids = ids[:limit]
		next = encodeDocumentCursor(layout.Base(), ids[len(ids)-1])
	}
	docs, err := r.readSourcePage(ctx, layout, ids, projection)
	return docs, next, err
}

func (r *Repo) readSourcePage(
	ctx context.Context, layout storage.Collection, ids []string, projection domdoc.SourceProjection,
) ([]domdoc.Document, error) {
	if len(ids) == 0 {
		return []domdoc.Document{}, nil
	}
	backend, ok := r.store.(projectionStore)
	if !ok {
		return nil, domain.ErrNotImplemented
	}
	fields := []string{"__source_version", "__expires_at", "__revision"}
	if projection.Content {
		fields = append(fields, "__content")
	}
	if projection.Metadata {
		fields = append(fields, "__source_metadata", "__metadata")
	}
	keys := make([]string, len(ids))
	for i, id := range ids {
		keys[i] = layout.Document(id)
	}
	rows, err := backend.HGetFieldsMulti(ctx, keys, fields)
	if err != nil {
		return nil, fmt.Errorf("read source projection: %w", err)
	}
	if len(rows) != len(ids) {
		return nil, fmt.Errorf("source projection response count mismatch")
	}
	docs := make([]domdoc.Document, 0, len(ids))
	for i := 0; i < len(ids) && i < len(rows); i++ {
		doc, present, err := parseSourceProjection(ids[i], rows[i], fields)
		if err != nil {
			return nil, err
		}
		if present {
			docs = append(docs, doc)
		}
	}
	return docs, nil
}

func restoreProjectedMetadata(fields map[string]string) error {
	if fields["__source_metadata"] != "" || fields["__metadata"] == "" {
		return nil
	}
	var projection db.RecordMetadata
	if err := json.Unmarshal([]byte(fields["__metadata"]), &projection); err != nil {
		return fmt.Errorf("decode legacy source metadata: %w", err)
	}
	values := make(map[string]any, len(projection.Tags)+len(projection.Numerics))
	for key, value := range projection.Tags {
		values[key] = value
	}
	for key, value := range projection.Numerics {
		values[key] = value
	}
	raw, err := json.Marshal(values)
	if err != nil {
		return fmt.Errorf("restore source metadata: %w", err)
	}
	fields["__source_metadata"] = string(raw)
	return nil
}

func parseSourceProjection(id string, row, fields []string) (domdoc.Document, bool, error) {
	if len(row) != len(fields) || len(row) < 3 {
		return domdoc.Document{}, false, fmt.Errorf("source projection response width mismatch")
	}
	if row[0] == "" {
		for _, value := range row[1:] {
			if value != "" {
				return domdoc.Document{}, false, fmt.Errorf("stored source has no version")
			}
		}
		return domdoc.Document{}, false, nil
	}
	values := make(map[string]string, len(fields))
	for j := 0; j < len(fields) && j < len(row); j++ {
		values[fields[j]] = row[j]
	}
	if err := restoreProjectedMetadata(values); err != nil {
		return domdoc.Document{}, false, err
	}
	doc, err := parseHashFields(id, values)
	if err != nil {
		return domdoc.Document{}, false, err
	}
	return doc, !domdoc.Expired(doc.ExpiresAt(), time.Now().UnixMilli()), nil
}
