package document

import (
	"context"
	"fmt"

	"github.com/kailas-cloud/vecdex/internal/domain"
	"github.com/kailas-cloud/vecdex/internal/domain/collection/field"
	domdoc "github.com/kailas-cloud/vecdex/internal/domain/document"
	"github.com/kailas-cloud/vecdex/internal/domain/document/patch"
	"github.com/kailas-cloud/vecdex/internal/domain/geo"
)

// Service handles document CRUD with automatic vectorization.
type Service struct {
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
	col, err := s.colls.Get(ctx, collectionName)
	if err != nil {
		return false, fmt.Errorf("get collection: %w", err)
	}

	if err := s.validateDocFields(doc, col); err != nil {
		return false, err
	}

	if col.IsGeo() {
		return s.upsertGeo(ctx, collectionName, doc)
	}

	result, err := s.docEmbedder.Embed(ctx, doc.Content())
	if err != nil {
		return false, fmt.Errorf("vectorize document: %w", err)
	}

	domain.UsageFromContext(ctx).AddTokens(result.TotalTokens)

	if col.VectorDim() > 0 && len(result.Embedding) != col.VectorDim() {
		return false, fmt.Errorf(
			"vector dimension mismatch: got %d, want %d: %w",
			len(result.Embedding), col.VectorDim(), domain.ErrVectorDimMismatch,
		)
	}

	doc.SetVector(result.Embedding)
	created, err := s.repo.Upsert(ctx, collectionName, doc)
	if err != nil {
		return false, fmt.Errorf("upsert document: %w", err)
	}

	return created, nil
}

// upsertGeo vectorizes a geo document from latitude/longitude numerics (no embedding API call).
func (s *Service) upsertGeo(ctx context.Context, collectionName string, doc *domdoc.Document) (bool, error) {
	lat, hasLat := doc.Numerics()["latitude"]
	lon, hasLon := doc.Numerics()["longitude"]
	if !hasLat || !hasLon {
		return false, fmt.Errorf("geo document requires latitude and longitude numerics: %w", domain.ErrInvalidSchema)
	}
	if !geo.ValidateCoordinates(lat, lon) {
		return false, fmt.Errorf("invalid coordinates: lat=%f lon=%f: %w", lat, lon, domain.ErrGeoQueryInvalid)
	}

	doc.SetVector(geo.ToVector(lat, lon))
	created, err := s.repo.Upsert(ctx, collectionName, doc)
	if err != nil {
		return false, fmt.Errorf("upsert geo document: %w", err)
	}
	return created, nil
}

// Get retrieves a document by collection and ID.
func (s *Service) Get(ctx context.Context, collectionName, id string) (domdoc.Document, error) {
	if _, err := s.colls.Get(ctx, collectionName); err != nil {
		return domdoc.Document{}, fmt.Errorf("get collection: %w", err)
	}

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
	if _, err := s.colls.Get(ctx, collectionName); err != nil {
		return nil, "", fmt.Errorf("get collection: %w", err)
	}

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
	if _, err := s.colls.Get(ctx, collectionName); err != nil {
		return fmt.Errorf("get collection: %w", err)
	}

	if err := s.repo.Delete(ctx, collectionName, id); err != nil {
		return fmt.Errorf("delete document: %w", err)
	}
	return nil
}

// Patch applies a partial update to a document.
func (s *Service) Patch(ctx context.Context, collectionName, id string, p patch.Patch) (domdoc.Document, error) {
	col, err := s.colls.Get(ctx, collectionName)
	if err != nil {
		return domdoc.Document{}, fmt.Errorf("get collection: %w", err)
	}

	// Validate patched fields against collection schema
	if err := s.validatePatchFields(p, col); err != nil {
		return domdoc.Document{}, err
	}

	// Re-vectorize when content changes (text) or coordinates change (geo)
	var newVector []float32
	if col.IsGeo() {
		newVector = geoVectorFromPatch(p)
	} else if p.HasContent() {
		result, embedErr := s.docEmbedder.Embed(ctx, *p.Content())
		if embedErr != nil {
			return domdoc.Document{}, fmt.Errorf("vectorize updated content: %w", embedErr)
		}
		newVector = result.Embedding
		domain.UsageFromContext(ctx).AddTokens(result.TotalTokens)
	}

	if err := s.repo.Patch(ctx, collectionName, id, p, newVector); err != nil {
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
	if _, err := s.colls.Get(ctx, collectionName); err != nil {
		return 0, fmt.Errorf("get collection: %w", err)
	}
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
	fieldTypes := make(map[string]field.Type)
	for _, f := range fields {
		fieldTypes[f.Name()] = f.FieldType()
	}

	for _, k := range tagKeys {
		ft, ok := fieldTypes[k]
		if !ok {
			return fmt.Errorf(
				"unknown field %q (not in collection schema): %w",
				k, domain.ErrInvalidSchema,
			)
		}
		if ft != field.Tag {
			return fmt.Errorf(
				"field %q is %s, not tag: %w",
				k, ft, domain.ErrInvalidSchema,
			)
		}
	}

	for _, k := range numericKeys {
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

// geoVectorFromPatch computes ECEF vector if latitude or longitude were patched.
// Returns nil if no coordinate change.
func geoVectorFromPatch(p patch.Patch) []float32 {
	nums := p.Numerics()
	latPtr, hasLat := nums["latitude"]
	lonPtr, hasLon := nums["longitude"]
	if !hasLat && !hasLon {
		return nil
	}
	// At least one coordinate changed — need both for re-vectorization.
	// The caller must ensure both are provided (or handle partial via read-modify-write).
	if hasLat && latPtr != nil && hasLon && lonPtr != nil {
		return geo.ToVector(*latPtr, *lonPtr)
	}
	return nil
}
