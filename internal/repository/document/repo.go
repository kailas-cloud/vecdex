package document

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/kailas-cloud/vecdex/internal/db"
	"github.com/kailas-cloud/vecdex/internal/domain"
	domdoc "github.com/kailas-cloud/vecdex/internal/domain/document"
	"github.com/kailas-cloud/vecdex/internal/domain/document/patch"
	"github.com/kailas-cloud/vecdex/internal/domain/metadata"
	"github.com/kailas-cloud/vecdex/internal/domain/storage"
)

// store is the consumer interface for documents (ISP).
type store interface {
	db.HashMutator
	db.PartitionCatalogStore
	HGetFieldMulti(ctx context.Context, keys []string, field string) ([]string, error)
	HGetAll(ctx context.Context, key string) (map[string]string, error)
	HGetAllMulti(ctx context.Context, keys []string) ([]map[string]string, error)
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
// Replacement and revision comparison are atomic.
func (r *Repo) Upsert(ctx context.Context, collectionName string, doc *domdoc.Document) (bool, error) {
	layout, err := storage.NewCollection(collectionName, domain.CollectionGeneration(ctx))
	if err != nil {
		return false, fmt.Errorf("document layout: %w", err)
	}
	fields, children, err := prepareFragmentFields(doc, layout, rand.Text())
	if err != nil {
		return false, err
	}
	p := storage.Partition(doc.ID())
	result, err := r.store.MutateHash(ctx, &db.HashMutation{
		Request:          requestForMutation(ctx),
		WorkQueueKey:     layout.WorkQueue(p),
		DeadlineKey:      layout.Deadlines(p),
		ExpiryMarkerKey:  layout.ExpiryMarker(doc.ID()),
		ExpiresAt:        doc.ExpiresAt(),
		Key:              layout.Document(doc.ID()),
		CatalogKey:       layout.Catalog(p),
		CatalogMember:    doc.ID(),
		StagingKey:       layout.Staging(doc.ID(), "prepare"),
		Set:              fields,
		Children:         children,
		Replace:          true,
		ExpectedRevision: domain.ExpectedRevision(ctx),
		ExpectedVersion:  domain.ExpectedVersion(ctx),
		GuardKey:         layout.Guard(p),
		GuardGeneration:  domain.CollectionGeneration(ctx),
		GuardState:       "active",
	})
	if err != nil {
		return false, fmt.Errorf("upsert document: %w", err)
	}
	doc.SetRevision(result.Revision)
	doc.SetVersion(result.Version)
	return result.Created, nil
}

// BatchUpsert replaces each document atomically, including removed metadata.
func (r *Repo) BatchUpsert(ctx context.Context, collectionName string, docs []domdoc.Document) error {
	if len(docs) == 0 {
		return nil
	}

	for i := range docs {
		if _, err := r.Upsert(ctx, collectionName, &docs[i]); err != nil {
			return fmt.Errorf("batch upsert item %s: %w", docs[i].ID(), err)
		}
	}
	return nil
}

// Get returns a document by ID.
func (r *Repo) Get(ctx context.Context, collectionName, id string) (domdoc.Document, error) {
	layout, err := storage.NewCollection(collectionName, domain.CollectionGeneration(ctx))
	if err != nil {
		return domdoc.Document{}, fmt.Errorf("document layout: %w", err)
	}
	key := layout.Document(id)
	for attempt := 0; attempt < 8; attempt++ {
		fields, err := r.store.HGetAll(ctx, key)
		if err != nil {
			return domdoc.Document{}, fmt.Errorf("read document: %w", err)
		}
		if len(fields) == 0 {
			return domdoc.Document{}, domain.ErrDocumentNotFound
		}
		doc, err := parseHashFields(id, fields)
		if err != nil {
			return domdoc.Document{}, err
		}
		if domdoc.Expired(doc.ExpiresAt(), time.Now().UnixMilli()) {
			return domdoc.Document{}, domain.ErrDocumentNotFound
		}
		vectors, missing, err := r.fragmentVectors(ctx, layout, id, fields)
		if err != nil {
			return domdoc.Document{}, err
		}
		if missing {
			current, err := r.store.HGetAll(ctx, key)
			if err != nil {
				return domdoc.Document{}, fmt.Errorf("check document replacement: %w", err)
			}
			if current["__chunk_version"] != fields["__chunk_version"] {
				continue
			}
			return domdoc.Document{}, fmt.Errorf("committed document fragment is missing")
		}
		if err := hydrateFragments(&doc, fields, vectors); err != nil {
			return domdoc.Document{}, err
		}
		return doc, nil
	}
	return domdoc.Document{}, fmt.Errorf("document changed repeatedly while reading fragments")
}

func (r *Repo) fragmentVectors(
	ctx context.Context, layout storage.Collection, id string, fields map[string]string,
) (values []string, missing bool, err error) {
	if fields["__parts"] == "" {
		return nil, false, nil
	}
	var rows []fragmentRow
	if err := json.Unmarshal([]byte(fields["__parts"]), &rows); err != nil {
		return nil, false, fmt.Errorf("decode fragments: %w", err)
	}
	version := fields["__chunk_version"]
	if len(rows) == 0 || version == "" {
		return nil, false, fmt.Errorf("invalid fragment manifest")
	}
	vectors := make([]string, 0, len(rows))
	for offset := 0; offset < len(rows); offset += 32 {
		count := min(32, len(rows)-offset)
		keys := make([]string, count)
		for i := range keys {
			keys[i] = layout.Fragment(id, version, offset+i)
		}
		batch, err := r.store.HGetFieldMulti(ctx, keys, "__vector")
		if err != nil {
			return nil, false, fmt.Errorf("read fragment vectors: %w", err)
		}
		if len(batch) != len(keys) {
			return nil, false, fmt.Errorf("fragment vector response mismatch")
		}
		for _, vector := range batch {
			if vector == "" {
				return nil, true, nil
			}
		}
		vectors = append(vectors, batch...)
	}
	return vectors, false, nil
}

// Delete removes a document.
func (r *Repo) Delete(ctx context.Context, collectionName, id string) error {
	layout, err := storage.NewCollection(collectionName, domain.CollectionGeneration(ctx))
	if err != nil {
		return fmt.Errorf("document layout: %w", err)
	}
	p := storage.Partition(id)
	_, err = r.store.MutateHash(ctx, &db.HashMutation{
		Request:          requestForMutation(ctx),
		WorkQueueKey:     layout.WorkQueue(p),
		DeadlineKey:      layout.Deadlines(p),
		ExpiryMarkerKey:  layout.ExpiryMarker(id),
		Key:              layout.Document(id),
		CatalogKey:       layout.Catalog(p),
		CatalogMember:    id,
		Delete:           true,
		RequireExists:    true,
		ExpectedRevision: domain.ExpectedRevision(ctx),
		ExpectedVersion:  domain.ExpectedVersion(ctx),
		GuardKey:         layout.Guard(p),
		GuardGeneration:  domain.CollectionGeneration(ctx),
		GuardState:       "active",
	})
	if err != nil {
		return fmt.Errorf("delete document: %w", err)
	}
	return nil
}

// Patch performs a partial update: HDEL removed fields, HSET updated fields.
// No full-key DEL — avoids race where concurrent Patch sees empty key.
func (r *Repo) Patch(
	ctx context.Context,
	collectionName, id string,
	p patch.Patch,
	newVector []float32,
	fragments []domdoc.Fragment,
) (domdoc.WriteResult, error) {
	for attempt := 0; attempt < 8; attempt++ {
		current, err := r.Get(ctx, collectionName, id)
		if err != nil {
			return domdoc.WriteResult{}, patchReadError(ctx, err)
		}
		if current.Metadata().Present() && (len(p.Tags()) > 0 || len(p.Numerics()) > 0) {
			return domdoc.WriteResult{}, fmt.Errorf("JSON metadata requires a metadata patch: %w", domain.ErrInvalidSchema)
		}
		updated, err := applyPatch(&current, p, newVector)
		if err != nil {
			return domdoc.WriteResult{}, err
		}
		if p.HasContent() {
			updated.SetFragments(fragments)
		}
		revision := current.Revision()
		writeCtx := ctx
		if domain.ExpectedRevision(ctx) == nil && domain.ExpectedVersion(ctx) == nil {
			version := current.Version()
			if version != "" {
				writeCtx = domain.WithExpectedVersion(ctx, &version)
			} else {
				writeCtx = domain.WithExpectedRevision(ctx, &revision)
			}
		}
		_, err = r.Upsert(writeCtx, collectionName, &updated)
		if err == nil {
			return domdoc.WriteResult{ID: id, Version: updated.Version()}, nil
		}
		if domain.ExpectedRevision(ctx) != nil || domain.ExpectedVersion(ctx) != nil ||
			!errors.Is(err, domain.ErrRevisionConflict) {
			return domdoc.WriteResult{}, err
		}
	}
	return domdoc.WriteResult{}, fmt.Errorf("concurrent patch retry limit: %w", domain.ErrRevisionConflict)
}

func patchReadError(ctx context.Context, err error) error {
	if errors.Is(err, domain.ErrDocumentNotFound) && domain.ExpectedVersion(ctx) != nil {
		return fmt.Errorf("source is absent: %w", domain.NewVersionConflict(""))
	}
	return err
}

func applyPatch(current *domdoc.Document, p patch.Patch, newVector []float32) (domdoc.Document, error) {
	content := current.Content()
	if p.HasContent() {
		content = *p.Content()
	}
	tags := applyFieldPatch(current.Tags(), p.Tags())
	numerics := applyFieldPatch(current.Numerics(), p.Numerics())
	vector := current.Vector()
	if newVector != nil {
		vector = newVector
	}
	updated := domdoc.Reconstruct(current.ID(), content, tags, numerics, vector, current.Revision())
	updated.SetFragments(current.Fragments())
	updated.SetMetadata(current.Metadata())
	if err := updated.SetExpiresAt(current.ExpiresAt()); err != nil {
		return domdoc.Document{}, fmt.Errorf("preserve source deadline: %w", err)
	}
	if expiresAt := p.ExpiresAt(); expiresAt != nil {
		if err := updated.SetExpiresAt(*expiresAt); err != nil {
			return domdoc.Document{}, fmt.Errorf("patch source deadline: %w", err)
		}
	}
	if p.HasMetadata() {
		value, err := metadata.Merge(current.Metadata(), p.Metadata())
		if err != nil {
			return domdoc.Document{}, fmt.Errorf("merge document metadata: %w", err)
		}
		updated.SetMetadata(value)
		if err := updated.ProjectMetadata(p.MetadataFields()); err != nil {
			return domdoc.Document{}, fmt.Errorf("project patched metadata: %w", err)
		}
	}
	return updated, nil
}

func applyFieldPatch[T any](fields map[string]T, updates map[string]*T) map[string]T {
	if fields == nil {
		fields = map[string]T{}
	}
	for key, value := range updates {
		if value == nil {
			delete(fields, key)
		} else {
			fields[key] = *value
		}
	}
	return fields
}
