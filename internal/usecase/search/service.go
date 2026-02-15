package search

import (
	"context"
	"fmt"

	"github.com/kailas-cloud/vecdex/internal/domain"
	domcol "github.com/kailas-cloud/vecdex/internal/domain/collection"
	"github.com/kailas-cloud/vecdex/internal/domain/collection/field"
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
}

// New creates a search service.
func New(repo Repository, colls CollectionReader, embed Embedder) *Service {
	return &Service{repo: repo, colls: colls, embed: embed}
}

// Search executes a document search across semantic, keyword, or hybrid modes.
func (s *Service) Search(
	ctx context.Context, collectionName string, req *request.Request,
) ([]result.Result, error) {
	col, err := s.colls.Get(ctx, collectionName)
	if err != nil {
		return nil, fmt.Errorf("get collection: %w", err)
	}

	if err = validateFiltersAgainstSchema(req.Filters(), col); err != nil {
		return nil, fmt.Errorf("%w: %w", domain.ErrInvalidSchema, err)
	}

	var results []result.Result

	switch req.Mode() {
	case mode.Semantic:
		results, err = s.searchSemantic(ctx, collectionName, req)
	case mode.Keyword:
		results, err = s.searchKeyword(ctx, collectionName, req)
	case mode.Hybrid:
		results, err = s.searchHybrid(ctx, collectionName, req)
	default:
		return nil, fmt.Errorf("unsupported search mode: %s", req.Mode())
	}
	if err != nil {
		return nil, err
	}

	// Post-filter: min_score
	if req.MinScore() > 0 {
		filtered := results[:0]
		for _, r := range results {
			if r.Score() >= req.MinScore() {
				filtered = append(filtered, r)
			}
		}
		results = filtered
	}

	// Limit
	if len(results) > req.Limit() {
		results = results[:req.Limit()]
	}

	return results, nil
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
		ctx, collectionName, embResult.Embedding, req.Filters(), req.TopK(), req.IncludeVectors(),
	)
	if err != nil {
		return nil, fmt.Errorf("search knn: %w", err)
	}
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
		ctx, collectionName, embResult.Embedding, req.Filters(), req.TopK(), req.IncludeVectors(),
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
