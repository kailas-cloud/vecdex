package search

import (
	"context"
	"encoding/binary"
	"fmt"
	"math"
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
	includeVectors bool, rawScores bool,
) ([]result.Result, error) {
	indexName := fmt.Sprintf("%s%s:idx", domain.KeyPrefix, collectionName)

	returnFields := []string{"__content", "__vector", "__vector_score"}

	q := &db.KNNQuery{
		IndexName:     indexName,
		Filters:       filters,
		Vector:        vector,
		K:             topK,
		ReturnFields:  returnFields,
		IncludeVector: includeVectors,
		RawScores:     rawScores,
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
		IndexName: indexName,
		Query:     query,
		Filters:   filters,
		TopK:      topK,
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

// parseEntryFields parses a KNN entry from flat hash fields.
func parseEntryFields(docID string, entry db.SearchEntry, includeVectors bool) result.Result {
	var content string
	var vector []float32
	tags := make(map[string]string)
	numerics := make(map[string]float64)

	for k, v := range entry.Fields {
		switch k {
		case "__content":
			content = v
		case "__vector":
			if includeVectors {
				vector = bytesToVector(v)
			}
		case "__vector_score":
			// handled by db layer via entry.Score
		default:
			if f, err := strconv.ParseFloat(v, 64); err == nil {
				numerics[k] = f
			} else {
				tags[k] = v
			}
		}
	}

	return result.New(docID, entry.Score, content, tags, numerics, vector)
}

// parseBM25EntryFields parses a BM25 entry from flat hash fields.
func parseBM25EntryFields(docID string, score float64, entry db.SearchEntry) result.Result {
	var content string
	tags := make(map[string]string)
	numerics := make(map[string]float64)

	for k, v := range entry.Fields {
		switch k {
		case "__content":
			content = v
		case "__vector":
			// skip vector in BM25
		default:
			if f, err := strconv.ParseFloat(v, 64); err == nil {
				numerics[k] = f
			} else {
				tags[k] = v
			}
		}
	}

	return result.New(docID, score, content, tags, numerics, nil)
}

// bytesToVector deserializes a binary string to []float32.
func bytesToVector(s string) []float32 {
	b := []byte(s)
	if len(b)%4 != 0 {
		return nil
	}
	v := make([]float32, len(b)/4)
	for i := range v {
		v[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[i*4:]))
	}
	return v
}
