package search

import (
	"context"
	"fmt"
	"sort"
	"sync"

	"github.com/kailas-cloud/vecdex/internal/domain"
	domcol "github.com/kailas-cloud/vecdex/internal/domain/collection"
	"github.com/kailas-cloud/vecdex/internal/domain/collection/field"
	"github.com/kailas-cloud/vecdex/internal/domain/search/filter"
	"github.com/kailas-cloud/vecdex/internal/domain/search/mode"
	"github.com/kailas-cloud/vecdex/internal/domain/search/request"
	"github.com/kailas-cloud/vecdex/internal/domain/search/result"
)

type asyncSearchResult struct {
	results []result.Result
	err     error
}

// Service handles document search across semantic, keyword, and hybrid modes.
type Service struct {
	repo  Repository
	colls CollectionReader
	embed Embedder
	docs  DocumentReader
	cfg   Config
}

// New creates a search service.
func New(repo Repository, colls CollectionReader, embed Embedder) *Service {
	return &Service{
		repo:  repo,
		colls: colls,
		embed: embed,
		cfg:   DefaultConfig(),
	}
}

// WithDocuments sets the document reader for Similar() functionality.
func (s *Service) WithDocuments(docs DocumentReader) *Service {
	s.docs = docs
	return s
}

// WithConfig overrides retrieval window defaults.
func (s *Service) WithConfig(cfg Config) *Service {
	s.cfg = normalizeConfig(cfg)
	return s
}

// Search executes a document search across semantic, keyword, and hybrid modes.
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

	results, err := s.dispatch(ctx, collectionName, req)
	if err != nil {
		return nil, 0, err
	}

	filtered, total := applyPostFilters(results, req.MinScore(), req.Limit())
	return filtered, total, nil
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
	default:
		return nil, fmt.Errorf("unsupported search mode: %s", req.Mode())
	}
}

// applyPostFilters applies min_score threshold and limit to search results.
// Returns filtered results and total count (after min_score, before limit).
func applyPostFilters(
	results []result.Result, minScore float64, limit int,
) (filtered []result.Result, total int) {
	if minScore > 0 {
		results = filterByScore(results, minScore)
	}
	total = len(results)
	if len(results) > limit {
		results = results[:limit]
	}
	return results, total
}

func filterByScore(results []result.Result, minScore float64) []result.Result {
	filtered := results[:0]
	for _, r := range results {
		if r.Score() >= minScore {
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
		ctx, collectionName, vector, req.Filters(), req.TopK()+1, req.IncludeVectors(),
	)
	if err != nil {
		return nil, 0, fmt.Errorf("search knn: %w", err)
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].Score() > results[j].Score()
	})

	results = excludeByID(results, documentID)
	out, total := applyPostFilters(results, req.MinScore(), req.Limit())
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
		ctx, collectionName, embResult.Embedding, req.Filters(),
		candidateWindow(
			req.TopK(),
			req.Limit(),
			s.cfg.SemanticCandidateFloor,
			s.cfg.SemanticCandidateMultiplier,
		),
		req.IncludeVectors(),
	)
	if err != nil {
		return nil, fmt.Errorf("search knn: %w", err)
	}

	// HNSW is approximate — enforce descending similarity order.
	sort.Slice(results, func(i, j int) bool {
		return results[i].Score() > results[j].Score()
	})

	return aggregateToDocuments(results, req.TopK()), nil
}

// searchKeyword runs BM25 search when the backend exposes a TEXT field.
func (s *Service) searchKeyword(
	ctx context.Context, collectionName string, req *request.Request,
) ([]result.Result, error) {
	if !s.repo.SupportsTextSearch(ctx) {
		return nil, domain.ErrKeywordSearchNotSupported
	}

	results, err := s.repo.SearchBM25(
		ctx, collectionName, req.Query(), req.Filters(),
		candidateWindow(
			req.TopK(),
			req.Limit(),
			s.cfg.BM25CandidateFloor,
			s.cfg.BM25CandidateMultiplier,
		),
	)
	if err != nil {
		return nil, fmt.Errorf("search bm25: %w", err)
	}
	return aggregateToDocuments(results, req.TopK()), nil
}

// searchHybrid runs KNN + BM25 in parallel, then fuses via RRF.
func (s *Service) searchHybrid(
	ctx context.Context, collectionName string, req *request.Request,
) ([]result.Result, error) {
	if !s.repo.SupportsTextSearch(ctx) {
		return nil, domain.ErrKeywordSearchNotSupported
	}

	semanticCandidateK := candidateWindow(
		req.TopK(),
		req.Limit(),
		s.cfg.SemanticCandidateFloor,
		s.cfg.SemanticCandidateMultiplier,
	)
	bm25CandidateK := candidateWindow(
		req.TopK(),
		req.Limit(),
		s.cfg.BM25CandidateFloor,
		s.cfg.BM25CandidateMultiplier,
	)

	semanticCh := make(chan asyncSearchResult, 1)
	keywordCh := make(chan asyncSearchResult, 1)

	var wg sync.WaitGroup
	wg.Add(2)
	go s.runHybridSemantic(ctx, collectionName, req, semanticCandidateK, semanticCh, &wg)
	go s.runHybridKeyword(ctx, collectionName, req, bm25CandidateK, keywordCh, &wg)

	semanticRes, keywordRes := waitHybridResults(semanticCh, keywordCh, &wg)

	if semanticRes.err != nil {
		return nil, semanticRes.err
	}
	if keywordRes.err != nil {
		return nil, keywordRes.err
	}

	fused := fuseRRF(semanticRes.results, keywordRes.results, 0)
	return aggregateToDocuments(fused, req.TopK()), nil
}

func (s *Service) runHybridSemantic(
	ctx context.Context,
	collectionName string,
	req *request.Request,
	candidateK int,
	out chan<- asyncSearchResult,
	wg *sync.WaitGroup,
) {
	defer wg.Done()

	embResult, err := s.embed.Embed(ctx, req.Query())
	if err != nil {
		out <- asyncSearchResult{err: fmt.Errorf("vectorize query: %w", err)}
		return
	}

	domain.UsageFromContext(ctx).AddTokens(embResult.TotalTokens)

	results, err := s.repo.SearchKNN(
		ctx, collectionName, embResult.Embedding, req.Filters(), candidateK, req.IncludeVectors(),
	)
	if err != nil {
		out <- asyncSearchResult{err: fmt.Errorf("search knn: %w", err)}
		return
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].Score() > results[j].Score()
	})

	out <- asyncSearchResult{results: results}
}

func (s *Service) runHybridKeyword(
	ctx context.Context,
	collectionName string,
	req *request.Request,
	candidateK int,
	out chan<- asyncSearchResult,
	wg *sync.WaitGroup,
) {
	defer wg.Done()

	results, err := s.repo.SearchBM25(
		ctx, collectionName, req.Query(), req.Filters(), candidateK,
	)
	if err != nil {
		out <- asyncSearchResult{err: fmt.Errorf("search bm25: %w", err)}
		return
	}

	out <- asyncSearchResult{results: results}
}

func waitHybridResults(
	semanticCh, keywordCh <-chan asyncSearchResult,
	wg *sync.WaitGroup,
) (semanticRes, keywordRes asyncSearchResult) {
	semanticRes = <-semanticCh
	keywordRes = <-keywordCh
	wg.Wait()
	return semanticRes, keywordRes
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
