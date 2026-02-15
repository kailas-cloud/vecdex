package search

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/kailas-cloud/vecdex/internal/db"
	"github.com/kailas-cloud/vecdex/internal/domain"
	"github.com/kailas-cloud/vecdex/internal/domain/search/filter"
	"github.com/kailas-cloud/vecdex/internal/domain/search/result"
)

// store is the consumer interface for search operations (ISP).
type store interface {
	SearchKNN(ctx context.Context, q *db.KNNQuery) (*db.SearchResult, error)
	SearchBM25(ctx context.Context, q *db.TextQuery) (*db.SearchResult, error)
	SupportsTextSearch(ctx context.Context) bool
}

// Repo implements usecase/search.Repository.
type Repo struct {
	store store
}

// New creates a search repository.
func New(s store) *Repo {
	return &Repo{store: s}
}

// SupportsTextSearch proxies the capability check from the store.
func (r *Repo) SupportsTextSearch(ctx context.Context) bool {
	return r.store.SupportsTextSearch(ctx)
}

// SearchKNN performs a KNN (vector similarity) search on a collection with filter pre-filtering.
func (r *Repo) SearchKNN(
	ctx context.Context, collectionName string,
	vector []float32, filters filter.Expression, topK int,
	includeVectors bool,
) ([]result.Result, error) {
	indexName := fmt.Sprintf("%s%s:idx", domain.KeyPrefix, collectionName)

	returnFields := []string{"$"}
	if includeVectors {
		returnFields = append(returnFields, "__vector_score")
	}

	q := &db.KNNQuery{
		IndexName:     indexName,
		Filters:       filters,
		Vector:        vector,
		K:             topK,
		ReturnFields:  returnFields,
		IncludeVector: includeVectors,
	}

	sr, err := r.store.SearchKNN(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("search knn %s: %w", collectionName, err)
	}

	return parseKNNResults(sr, collectionName, includeVectors)
}

// SearchBM25 performs a BM25 keyword search (requires a TEXT field in the index).
func (r *Repo) SearchBM25(
	ctx context.Context, collectionName string,
	query string, filters filter.Expression, topK int,
) ([]result.Result, error) {
	indexName := fmt.Sprintf("%s%s:idx", domain.KeyPrefix, collectionName)

	q := &db.TextQuery{
		IndexName:    indexName,
		Query:        query,
		Filters:      filters,
		TopK:         topK,
		ReturnFields: []string{"$"},
	}

	sr, err := r.store.SearchBM25(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("search bm25 %s: %w", collectionName, err)
	}

	return parseBM25Results(sr, collectionName)
}

// parseKNNResults converts db.SearchResult into []result.Result.
func parseKNNResults(sr *db.SearchResult, collection string, includeVectors bool) ([]result.Result, error) {
	if sr == nil || sr.Total == 0 {
		return nil, nil
	}

	prefix := fmt.Sprintf("%s%s:", domain.KeyPrefix, collection)
	results := make([]result.Result, 0, len(sr.Entries))

	for _, entry := range sr.Entries {
		docID := strings.TrimPrefix(entry.Key, prefix)
		res := parseEntryFields(docID, entry, includeVectors)
		results = append(results, res)
	}

	return results, nil
}

// parseBM25Results converts db.SearchResult into []result.Result.
func parseBM25Results(sr *db.SearchResult, collection string) ([]result.Result, error) {
	if sr == nil || sr.Total == 0 {
		return nil, nil
	}

	prefix := fmt.Sprintf("%s%s:", domain.KeyPrefix, collection)
	results := make([]result.Result, 0, len(sr.Entries))

	for _, entry := range sr.Entries {
		docID := strings.TrimPrefix(entry.Key, prefix)
		res := parseBM25EntryFields(docID, entry.Score, entry)
		results = append(results, res)
	}

	return results, nil
}

// parseEntryFields parses a KNN entry: score from __vector_score, content/tags/numerics from $.
func parseEntryFields(docID string, entry db.SearchEntry, includeVectors bool) result.Result {
	var score float64
	var content string
	var vector []float32
	tags := make(map[string]string)
	numerics := make(map[string]float64)

	// __vector_score: cosine distance; similarity = 1.0 - distance
	if scoreStr, ok := entry.Fields["__vector_score"]; ok {
		if s, err := strconv.ParseFloat(scoreStr, 64); err == nil {
			score = 1.0 - s
		}
	}

	// $ field: JSON document
	if jsonStr, ok := entry.Fields["$"]; ok {
		parseJSONField(jsonStr, &content, tags, numerics, includeVectors, &vector)
	}

	return result.New(docID, score, content, tags, numerics, vector)
}

// parseBM25EntryFields parses a BM25 entry: score from SearchEntry.Score, content/tags/numerics from $.
func parseBM25EntryFields(docID string, score float64, entry db.SearchEntry) result.Result {
	var content string
	tags := make(map[string]string)
	numerics := make(map[string]float64)

	if jsonStr, ok := entry.Fields["$"]; ok {
		parseJSONField(jsonStr, &content, tags, numerics, false, nil)
	}

	return result.New(docID, score, content, tags, numerics, nil)
}

// parseJSONField parses the JSON string from the $ field of a search result.
func parseJSONField(
	jsonStr string, content *string,
	tags map[string]string, numerics map[string]float64,
	includeVectors bool, vector *[]float32,
) {
	var m map[string]any
	if err := json.Unmarshal([]byte(jsonStr), &m); err != nil {
		return
	}
	for k, v := range m {
		switch k {
		case "__content":
			if s, ok := v.(string); ok {
				*content = s
			}
		case "__vector":
			if includeVectors && vector != nil {
				if arr, ok := v.([]any); ok {
					vec := make([]float32, 0, len(arr))
					for _, elem := range arr {
						if f, ok := elem.(float64); ok {
							vec = append(vec, float32(f))
						}
					}
					*vector = vec
				}
			}
		default:
			switch val := v.(type) {
			case string:
				tags[k] = val
			case float64:
				numerics[k] = val
			}
		}
	}
}
