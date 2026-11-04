package document

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"

	"github.com/kailas-cloud/vecdex/internal/db"
	"github.com/kailas-cloud/vecdex/internal/domain"
	domdoc "github.com/kailas-cloud/vecdex/internal/domain/document"
	"github.com/kailas-cloud/vecdex/internal/domain/document/patch"
)

// store is the consumer interface for documents (ISP).
type store interface {
	db.HashMutator
	db.CatalogStore
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
	base := domain.CollectionDataBase(collectionName, domain.CollectionGeneration(ctx))
	fields, children, err := prepareFragmentFields(doc, base, rand.Text())
	if err != nil {
		return false, err
	}
	result, err := r.store.MutateHash(ctx, &db.HashMutation{
		Key:           documentKey(ctx, collectionName, doc.ID()),
		CatalogKey:    domain.DocumentCatalogKey(collectionName, domain.CollectionGeneration(ctx)),
		CatalogMember: doc.ID(),
		StagingKey: domain.CollectionDataBase(
			collectionName,
			domain.CollectionGeneration(ctx),
		) + ":pending:" + doc.ID(),
		Set:              fields,
		Children:         children,
		Replace:          true,
		ExpectedRevision: domain.ExpectedRevision(ctx),
		GuardKey: domain.CollectionMetadataKey(
			collectionName,
		),
		GuardGeneration: domain.CollectionGeneration(ctx),
		GuardState:      "active",
	})
	if err != nil {
		return false, fmt.Errorf("upsert document: %w", err)
	}
	doc.SetRevision(result.Revision)
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
	key := documentKey(ctx, collectionName, id)
	m, err := r.store.HGetAll(ctx, key)
	if err != nil {
		return domdoc.Document{}, fmt.Errorf("hgetall %s: %w", key, err)
	}
	if len(m) == 0 {
		return domdoc.Document{}, domain.ErrDocumentNotFound
	}
	doc := parseHashFields(id, m)
	if err := hydrateFragments(&doc, m); err != nil {
		return domdoc.Document{}, err
	}
	return doc, nil
}

// Delete removes a document.
func (r *Repo) Delete(ctx context.Context, collectionName, id string) error {
	_, err := r.store.MutateHash(ctx, &db.HashMutation{
		Key: documentKey(
			ctx,
			collectionName,
			id,
		),
		StagingKey: domain.CollectionDataBase(
			collectionName,
			domain.CollectionGeneration(ctx),
		) + ":pending:" + id,
		CatalogKey:       domain.DocumentCatalogKey(collectionName, domain.CollectionGeneration(ctx)),
		CatalogMember:    id,
		Delete:           true,
		RequireExists:    true,
		ExpectedRevision: domain.ExpectedRevision(ctx),
		GuardKey: domain.CollectionMetadataKey(
			collectionName,
		),
		GuardGeneration: domain.CollectionGeneration(ctx),
		GuardState:      "active",
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
) error {
	for attempt := 0; attempt < 8; attempt++ {
		current, err := r.Get(ctx, collectionName, id)
		if err != nil {
			return err
		}
		updated := applyPatch(current, p, newVector)
		if p.HasContent() {
			updated.SetFragments(fragments)
		}
		revision := current.Revision()
		writeCtx := ctx
		if domain.ExpectedRevision(ctx) == nil {
			writeCtx = domain.WithExpectedRevision(ctx, &revision)
		}
		_, err = r.Upsert(writeCtx, collectionName, &updated)
		if err == nil {
			return nil
		}
		if domain.ExpectedRevision(ctx) != nil || !errors.Is(err, domain.ErrRevisionConflict) {
			return err
		}
	}
	return fmt.Errorf("concurrent patch retry limit: %w", domain.ErrRevisionConflict)
}

func applyPatch(current domdoc.Document, p patch.Patch, newVector []float32) domdoc.Document {
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
	return updated
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

func documentKey(ctx context.Context, collection, id string) string {
	return domain.GenerationDocumentPrefix(collection, domain.CollectionGeneration(ctx)) + id
}
