package document

import (
	"context"
	"fmt"

	"github.com/kailas-cloud/vecdex/internal/domain"
	domcol "github.com/kailas-cloud/vecdex/internal/domain/collection"
	"github.com/kailas-cloud/vecdex/internal/domain/collection/field"
	domdoc "github.com/kailas-cloud/vecdex/internal/domain/document"
	"github.com/kailas-cloud/vecdex/internal/domain/document/patch"
	"github.com/kailas-cloud/vecdex/internal/usecase/ingestion"
)

// Service handles document CRUD with automatic vectorization.
type Service struct {
	embeddingSpace  string
	repo            Repository
	colls           CollectionReader
	docEmbedder     Embedder
	queryEmbedder   Embedder
	defaultPageSize int
	maxPageSize     int
}

// New creates a document service.
func New(repo Repository, colls CollectionReader, docEmbedder, queryEmbedder Embedder) *Service {
	return &Service{
		repo:            repo,
		colls:           colls,
		docEmbedder:     docEmbedder,
		queryEmbedder:   queryEmbedder,
		defaultPageSize: 20,
		maxPageSize:     100,
	}
}

// WithPagination configures page size limits.
func (s *Service) WithPagination(defaultPageSize, maxPageSize int) *Service {
	if defaultPageSize > 0 {
		s.defaultPageSize = defaultPageSize
	}
	if maxPageSize > 0 {
		s.maxPageSize = maxPageSize
	}
	return s
}

// Upsert creates or updates a document with automatic vectorization.
// Returns true if the document was created, false if updated.
func (s *Service) Upsert(ctx context.Context, collectionName string, doc *domdoc.Document) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, ingestion.DocumentTimeout)
	defer cancel()
	col, err := s.colls.Get(ctx, collectionName)
	if err != nil {
		return false, fmt.Errorf("get collection: %w", err)
	}

	if err := col.CheckEmbeddingSpace(s.embeddingSpace); err != nil {
		return false, fmt.Errorf("upsert embedding space: %w", err)
	}
	ctx = domain.WithCollectionGeneration(ctx, col.Generation())
	if err := s.validateDocFields(doc, col); err != nil {
		return false, err
	}

	if doc.Content() == "" {
		return false, fmt.Errorf("content is required: %w", domain.ErrInvalidSchema)
	}

	prepared, err := ingestion.Prepare(ctx, s.docEmbedder, doc.Content(), col.VectorDim())
	domain.UsageFromContext(ctx).AddEmbedding(prepared.TotalTokens, prepared.UsageUnknown)
	if err != nil {
		return false, fmt.Errorf("prepare document: %w", err)
	}
	doc.SetVector(prepared.Vector)
	doc.SetFragments(prepared.Fragments)

	created, err := s.repo.Upsert(ctx, collectionName, doc)
	if err != nil {
		return false, fmt.Errorf("upsert document: %w", err)
	}

	return created, nil
}

// Get retrieves a document by collection and ID.
func (s *Service) Get(ctx context.Context, collectionName, id string) (domdoc.Document, error) {
	col, err := s.colls.Get(ctx, collectionName)
	if err != nil {
		return domdoc.Document{}, fmt.Errorf("get collection: %w", err)
	}

	ctx = domain.WithCollectionGeneration(ctx, col.Generation())
	doc, err := s.repo.Get(ctx, collectionName, id)
	if err != nil {
		return domdoc.Document{}, fmt.Errorf("get document: %w", err)
	}
	return doc, nil
}

// List returns a paginated list of documents.
func (s *Service) List(
	ctx context.Context, collectionName, cursor string, limit int,
) ([]domdoc.Document, string, error) {
	col, err := s.colls.Get(ctx, collectionName)
	if err != nil {
		return nil, "", fmt.Errorf("get collection: %w", err)
	}

	ctx = domain.WithCollectionGeneration(ctx, col.Generation())
	if limit <= 0 {
		limit = s.defaultPageSize
	}
	if limit > s.maxPageSize {
		limit = s.maxPageSize
	}

	docs, nextCursor, err := s.repo.List(ctx, collectionName, cursor, limit)
	if err != nil {
		return nil, "", fmt.Errorf("list documents: %w", err)
	}
	return docs, nextCursor, nil
}

// Delete removes a document.
func (s *Service) Delete(ctx context.Context, collectionName, id string) error {
	col, err := s.colls.Get(ctx, collectionName)
	if err != nil {
		return fmt.Errorf("get collection: %w", err)
	}

	ctx = domain.WithCollectionGeneration(ctx, col.Generation())
	if err := s.repo.Delete(ctx, collectionName, id); err != nil {
		return fmt.Errorf("delete document: %w", err)
	}
	return nil
}

// Patch applies a partial update to a document.
func (s *Service) Patch(ctx context.Context, collectionName, id string, p patch.Patch) (domdoc.Document, error) {
	ctx, cancel := context.WithTimeout(ctx, ingestion.DocumentTimeout)
	defer cancel()
	col, err := s.colls.Get(ctx, collectionName)
	if err != nil {
		return domdoc.Document{}, fmt.Errorf("get collection: %w", err)
	}

	ctx = domain.WithCollectionGeneration(ctx, col.Generation())
	// Validate patched fields against collection schema
	if err := s.validatePatchFields(p, col); err != nil {
		return domdoc.Document{}, err
	}

	// Re-vectorize when content changes.
	var newVector []float32
	var fragments []domdoc.Fragment
	if p.HasContent() {
		if err := col.CheckEmbeddingSpace(s.embeddingSpace); err != nil {
			return domdoc.Document{}, fmt.Errorf("patch embedding space: %w", err)
		}
		prepared, err := ingestion.Prepare(ctx, s.docEmbedder, *p.Content(), col.VectorDim())
		domain.UsageFromContext(ctx).AddEmbedding(prepared.TotalTokens, prepared.UsageUnknown)
		if err != nil {
			return domdoc.Document{}, fmt.Errorf("prepare updated document: %w", err)
		}
		newVector = prepared.Vector
		fragments = prepared.Fragments
	}

	if err := s.repo.Patch(ctx, collectionName, id, p, newVector, fragments); err != nil {
		return domdoc.Document{}, fmt.Errorf("patch document: %w", err)
	}

	// Return the updated document
	updated, err := s.repo.Get(ctx, collectionName, id)
	if err != nil {
		return domdoc.Document{}, fmt.Errorf("get patched document: %w", err)
	}
	return updated, nil
}

// Count returns the number of documents in a collection.
func (s *Service) Count(ctx context.Context, collectionName string) (int, error) {
	col, err := s.colls.Get(ctx, collectionName)
	if err != nil {
		return 0, fmt.Errorf("get collection: %w", err)
	}
	ctx = domain.WithCollectionGeneration(ctx, col.Generation())
	count, err := s.repo.Count(ctx, collectionName)
	if err != nil {
		return 0, fmt.Errorf("count documents: %w", err)
	}
	return count, nil
}

// validateDocFields checks Tags/Numerics against the collection schema.
func (s *Service) validateDocFields(
	doc *domdoc.Document, col interface{ Fields() []field.Field },
) error {
	return validateSchemaFields(
		keysStr(doc.Tags()), keysFloat(doc.Numerics()), col.Fields(),
	)
}

// validatePatchFields checks patch fields against the collection schema.
func (s *Service) validatePatchFields(
	p patch.Patch, col interface{ Fields() []field.Field },
) error {
	return validateSchemaFields(
		keysPtrStr(p.Tags()), keysPtrFloat(p.Numerics()), col.Fields(),
	)
}

func validateSchemaFields(
	tagKeys, numericKeys []string, fields []field.Field,
) error {
	fieldTypes := make(map[string]field.Type, len(fields)+len(domcol.SystemFields()))
	for _, f := range fields {
		fieldTypes[f.Name()] = f.FieldType()
	}
	for _, f := range domcol.SystemFields() {
		fieldTypes[f.Name()] = f.FieldType()
	}

	for _, k := range tagKeys {
		if field.IsInternalName(k) {
			return fmt.Errorf("field %q is reserved: %w", k, domain.ErrInvalidSchema)
		}
		ft, ok := fieldTypes[k]
		if !ok {
			continue // stored (non-indexed) field, allowed
		}
		if ft != field.Tag {
			return fmt.Errorf(
				"field %q is %s, not tag: %w",
				k, ft, domain.ErrInvalidSchema,
			)
		}
	}

	for _, k := range numericKeys {
		if field.IsInternalName(k) {
			return fmt.Errorf("field %q is reserved: %w", k, domain.ErrInvalidSchema)
		}
		ft, ok := fieldTypes[k]
		if !ok {
			return fmt.Errorf(
				"unknown field %q (not in collection schema): %w",
				k, domain.ErrInvalidSchema,
			)
		}
		if ft != field.Numeric {
			return fmt.Errorf(
				"field %q is %s, not numeric: %w",
				k, ft, domain.ErrInvalidSchema,
			)
		}
	}

	return nil
}

func keysStr(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

func keysFloat(m map[string]float64) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

func keysPtrStr(m map[string]*string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

func keysPtrFloat(m map[string]*float64) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

// WithEmbeddingSpace binds vector operations to the configured embedding space.
func (s *Service) WithEmbeddingSpace(fingerprint string) *Service {
	s.embeddingSpace = fingerprint
	return s
}
