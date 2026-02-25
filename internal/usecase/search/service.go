package search

import (
	"context"
	"fmt"
	"math"
	"sort"

	"github.com/kailas-cloud/vecdex/internal/domain"
	domcol "github.com/kailas-cloud/vecdex/internal/domain/collection"
	"github.com/kailas-cloud/vecdex/internal/domain/collection/field"
	"github.com/kailas-cloud/vecdex/internal/domain/geo"
	"github.com/kailas-cloud/vecdex/internal/domain/search/filter"
	"github.com/kailas-cloud/vecdex/internal/domain/search/mode"
	"github.com/kailas-cloud/vecdex/internal/domain/search/request"
	"github.com/kailas-cloud/vecdex/internal/domain/search/result"
)

// Service handles document search across semantic, keyword, and hybrid modes.
type Service struct {
	repo  Repository
	colls CollectionReader
	embed Embedder
	docs  DocumentReader
}

// New creates a search service.
func New(repo Repository, colls CollectionReader, embed Embedder) *Service {
	return &Service{repo: repo, colls: colls, embed: embed}
}

// WithDocuments sets the document reader for Similar() functionality.
func (s *Service) WithDocuments(docs DocumentReader) *Service {
	s.docs = docs
	return s
}

// Search executes a document search across semantic, keyword, hybrid, or geo modes.
// Returns results (post-filtered and limited), total candidates (post min_score, pre limit), and error.
func (s *Service) Search(
	ctx context.Context, collectionName string, req *request.Request,
) ([]result.Result, int, error) {
	col, err := s.colls.Get(ctx, collectionName)
	if err != nil {
		return nil, 0, fmt.Errorf("get collection: %w", err)
	}

	if err = validateFiltersAgainstSchema(req.Filters(), col); err != nil {
		return nil, 0, fmt.Errorf("%w: %w", domain.ErrInvalidSchema, err)
	}
	if err := validateSearchMode(req.Mode(), col.IsGeo()); err != nil {
		return nil, 0, err
	}

	results, err := s.dispatch(ctx, collectionName, req)
	if err != nil {
		return nil, 0, err
	}

	filtered, total := applyPostFilters(results, req.MinScore(), req.Limit(), req.Mode())
	return filtered, total, nil
}

// validateSearchMode ensures the search mode matches the collection type.
func validateSearchMode(m mode.Mode, isGeo bool) error {
	if m == mode.Geo && !isGeo {
		return fmt.Errorf("geo search on text collection: %w", domain.ErrCollectionTypeMismatch)
	}
	if m != mode.Geo && isGeo {
		return fmt.Errorf("%s search on geo collection: %w", m, domain.ErrCollectionTypeMismatch)
	}
	return nil
}

// dispatch routes to the appropriate search implementation.
func (s *Service) dispatch(
	ctx context.Context, collectionName string, req *request.Request,
) ([]result.Result, error) {
	switch req.Mode() {
	case mode.Semantic:
		return s.searchSemantic(ctx, collectionName, req)
	case mode.Keyword:
		return s.searchKeyword(ctx, collectionName, req)
	case mode.Hybrid:
		return s.searchHybrid(ctx, collectionName, req)
	case mode.Geo:
		return s.searchGeo(ctx, collectionName, req)
	default:
		return nil, fmt.Errorf("unsupported search mode: %s", req.Mode())
	}
}

// applyPostFilters applies min_score threshold and limit to search results.
// Returns filtered results and total count (after min_score, before limit).
// For geo mode, min_score is a max-distance threshold (lower = closer),
// so we keep results with score <= minScore. For other modes, higher is better.
func applyPostFilters(
	results []result.Result, minScore float64, limit int, m mode.Mode,
) (filtered []result.Result, total int) {
	if minScore > 0 {
		results = filterByScore(results, minScore, m)
	}
	total = len(results)
	if len(results) > limit {
		results = results[:limit]
	}
	return results, total
}

func filterByScore(results []result.Result, minScore float64, m mode.Mode) []result.Result {
	filtered := results[:0]
	for _, r := range results {
		if m == mode.Geo && r.Score() <= minScore {
			filtered = append(filtered, r)
		} else if m != mode.Geo && r.Score() >= minScore {
			filtered = append(filtered, r)
		}
	}
	return filtered
}

// Similar finds documents similar to a given document by reusing its stored vector.
// Only supported on text collections. Returns results (post-filtered), total, and error.
func (s *Service) Similar(
	ctx context.Context, collectionName, documentID string, req *request.SimilarRequest,
) ([]result.Result, int, error) {
	vector, err := s.loadDocumentVector(ctx, collectionName, documentID, req.Filters())
	if err != nil {
		return nil, 0, err
	}

	// Reuse stored vector — zero embedding cost. TopK+1 to compensate for source exclusion.
	results, err := s.repo.SearchKNN(
		ctx, collectionName, vector, req.Filters(), req.TopK()+1, req.IncludeVectors(), false,
	)
	if err != nil {
		return nil, 0, fmt.Errorf("search knn: %w", err)
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].Score() > results[j].Score()
	})

	results = excludeByID(results, documentID)
	out, total := applyPostFilters(results, req.MinScore(), req.Limit(), mode.Semantic)
	return out, total, nil
}

// loadDocumentVector validates the collection/document and returns the stored vector.
func (s *Service) loadDocumentVector(
	ctx context.Context, collectionName, documentID string, filters filter.Expression,
) ([]float32, error) {
	col, err := s.colls.Get(ctx, collectionName)
	if err != nil {
		return nil, fmt.Errorf("get collection: %w", err)
	}
	if col.IsGeo() {
		return nil, fmt.Errorf("similar on geo collection: %w", domain.ErrCollectionTypeMismatch)
	}
	if err := validateFiltersAgainstSchema(filters, col); err != nil {
		return nil, fmt.Errorf("%w: %w", domain.ErrInvalidSchema, err)
	}

	doc, err := s.docs.Get(ctx, collectionName, documentID)
	if err != nil {
		return nil, fmt.Errorf("get document: %w", err)
	}
	if len(doc.Vector()) == 0 {
		return nil, fmt.Errorf("document has no vector: %w", domain.ErrDocumentNotFound)
	}
	return doc.Vector(), nil
}

// excludeByID removes a single document by ID from results.
func excludeByID(results []result.Result, id string) []result.Result {
	filtered := make([]result.Result, 0, len(results))
	for _, r := range results {
		if r.ID() != id {
			filtered = append(filtered, r)
		}
	}
	return filtered
}

// searchSemantic embeds the query and runs KNN search (works on any backend).
func (s *Service) searchSemantic(
	ctx context.Context, collectionName string, req *request.Request,
) ([]result.Result, error) {
	embResult, err := s.embed.Embed(ctx, req.Query())
	if err != nil {
		return nil, fmt.Errorf("vectorize query: %w", err)
	}

	domain.UsageFromContext(ctx).AddTokens(embResult.TotalTokens)

	results, err := s.repo.SearchKNN(
		ctx, collectionName, embResult.Embedding, req.Filters(), req.TopK(), req.IncludeVectors(), false,
	)
	if err != nil {
		return nil, fmt.Errorf("search knn: %w", err)
	}

	// HNSW is approximate — enforce descending similarity order.
	sort.Slice(results, func(i, j int) bool {
		return results[i].Score() > results[j].Score()
	})

	return results, nil
}

// searchKeyword runs BM25 search (requires TEXT field, Redis 8.4+ only).
func (s *Service) searchKeyword(
	ctx context.Context, collectionName string, req *request.Request,
) ([]result.Result, error) {
	if !s.repo.SupportsTextSearch(ctx) {
		return nil, domain.ErrKeywordSearchNotSupported
	}

	results, err := s.repo.SearchBM25(
		ctx, collectionName, req.Query(), req.Filters(), req.TopK(),
	)
	if err != nil {
		return nil, fmt.Errorf("search bm25: %w", err)
	}
	return results, nil
}

// searchHybrid runs KNN + BM25 in parallel, then fuses via RRF (requires TEXT field).
func (s *Service) searchHybrid(
	ctx context.Context, collectionName string, req *request.Request,
) ([]result.Result, error) {
	if !s.repo.SupportsTextSearch(ctx) {
		return nil, domain.ErrKeywordSearchNotSupported
	}

	embResult, err := s.embed.Embed(ctx, req.Query())
	if err != nil {
		return nil, fmt.Errorf("vectorize query: %w", err)
	}

	domain.UsageFromContext(ctx).AddTokens(embResult.TotalTokens)

	knnResults, err := s.repo.SearchKNN(
		ctx, collectionName, embResult.Embedding, req.Filters(), req.TopK(), req.IncludeVectors(), false,
	)
	if err != nil {
		return nil, fmt.Errorf("search knn: %w", err)
	}

	bm25Results, err := s.repo.SearchBM25(
		ctx, collectionName, req.Query(), req.Filters(), req.TopK(),
	)
	if err != nil {
		return nil, fmt.Errorf("search bm25: %w", err)
	}

	return fuseRRF(knnResults, bm25Results, req.TopK()), nil
}

// searchGeo converts lat/lon query to ECEF, runs KNN, converts L2 distances to meters.
func (s *Service) searchGeo(
	ctx context.Context, collectionName string, req *request.Request,
) ([]result.Result, error) {
	gq := req.GeoQuery()
	if gq == nil {
		return nil, fmt.Errorf("geo search requires geo_query: %w", domain.ErrGeoQueryInvalid)
	}
	if !geo.ValidateCoordinates(gq.Latitude, gq.Longitude) {
		return nil, fmt.Errorf(
			"invalid coordinates: lat=%f lon=%f: %w",
			gq.Latitude, gq.Longitude, domain.ErrGeoQueryInvalid,
		)
	}

	vector := geo.ToVector(gq.Latitude, gq.Longitude)

	results, err := s.repo.SearchKNN(
		ctx, collectionName, vector, req.Filters(), req.TopK(), req.IncludeVectors(), true,
	)
	if err != nil {
		return nil, fmt.Errorf("search geo knn: %w", err)
	}

	// Redis/Valkey returns L2² (squared Euclidean distance) for L2 metric.
	// Convert: L2² → L2 (sqrt) → great-circle meters (Haversine).
	converted := make([]result.Result, len(results))
	for i, r := range results {
		l2 := math.Sqrt(r.Score())
		meters := geo.L2ToHaversineMeters(l2)
		converted[i] = result.New(
			r.ID(), meters, r.Content(),
			r.Tags(), r.Numerics(), r.Vector(),
		)
	}

	sort.Slice(converted, func(i, j int) bool {
		return converted[i].Score() < converted[j].Score()
	})

	return converted, nil
}

// validateFiltersAgainstSchema ensures filter fields exist in the collection
// and that filter type (match/range) matches the field type (tag/numeric).
func validateFiltersAgainstSchema(expr filter.Expression, col domcol.Collection) error {
	if expr.IsEmpty() {
		return nil
	}
	groups := [][]filter.Condition{expr.Must(), expr.Should(), expr.MustNot()}
	for _, conditions := range groups {
		for _, c := range conditions {
			f, ok := col.FieldByName(c.Key())
			if !ok {
				return fmt.Errorf("unknown filter field %q", c.Key())
			}
			if c.IsMatch() && f.FieldType() != field.Tag {
				return fmt.Errorf("match filter on non-tag field %q", c.Key())
			}
			if c.IsRange() && f.FieldType() != field.Numeric {
				return fmt.Errorf("range filter on non-numeric field %q", c.Key())
			}
		}
	}
	return nil
}
