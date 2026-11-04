package search

import (
	"context"
	"encoding/binary"
	"encoding/json"
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
	HGetFieldMulti(ctx context.Context, keys []string, field string) ([]string, error)
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
	indexName := domain.GenerationIndexName(collectionName, domain.CollectionGeneration(ctx))

	q := &db.KNNQuery{
		IndexName:       indexName,
		ExcludeParentID: domain.ExcludedDocument(ctx),
		Filters:         filters,
		Vector:          vector,
		K:               topK,
		IncludeVector:   includeVectors,
		ReturnFields:    searchReturnFields(ctx, includeVectors, true),
	}

	sr, err := r.store.SearchKNN(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("search knn %s: %w", collectionName, err)
	}

	if err := r.filterCommitted(ctx, collectionName, sr); err != nil {
		return nil, err
	}
	return parseKNNResults(sr, collectionName, includeVectors)
}

// SearchBM25 performs a BM25 keyword search (requires a TEXT field in the index).
func (r *Repo) SearchBM25(
	ctx context.Context, collectionName string,
	query string, filters filter.Expression, topK int,
) ([]result.Result, error) {
	indexName := domain.GenerationIndexName(collectionName, domain.CollectionGeneration(ctx))

	q := &db.TextQuery{
		IndexName:    indexName,
		Query:        query,
		Filters:      filters,
		TopK:         topK,
		ReturnFields: searchReturnFields(ctx, false, false),
	}

	sr, err := r.store.SearchBM25(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("search bm25 %s: %w", collectionName, err)
	}

	if err := r.filterCommitted(ctx, collectionName, sr); err != nil {
		return nil, err
	}
	return parseBM25Results(sr, collectionName)
}

// parseKNNResults converts db.SearchResult into []result.Result.
func parseKNNResults(sr *db.SearchResult, _ string, includeVectors bool) ([]result.Result, error) {
	if sr == nil || sr.Total == 0 {
		return nil, nil
	}

	results := make([]result.Result, 0, len(sr.Entries))

	for _, entry := range sr.Entries {
		docID := entry.Key[strings.LastIndex(entry.Key, ":")+1:]
		if entry.Fields["__parent_id"] != "" {
			docID = entry.Fields["__parent_id"]
		}
		res := parseEntryFields(docID, entry, includeVectors)
		results = append(results, res)
	}

	return results, nil
}

// parseBM25Results converts db.SearchResult into []result.Result.
func parseBM25Results(sr *db.SearchResult, _ string) ([]result.Result, error) {
	if sr == nil || sr.Total == 0 {
		return nil, nil
	}

	results := make([]result.Result, 0, len(sr.Entries))

	for _, entry := range sr.Entries {
		docID := entry.Key[strings.LastIndex(entry.Key, ":")+1:]
		if entry.Fields["__parent_id"] != "" {
			docID = entry.Fields["__parent_id"]
		}
		res := parseBM25EntryFields(docID, entry.Score, entry)
		results = append(results, res)
	}

	return results, nil
}

// numericPrefix disambiguates numeric fields from tags in HASH storage.
const numericPrefix = "__n:"

// parseEntryFields parses a KNN entry from flat hash fields.
func parseEntryFields(docID string, entry db.SearchEntry, includeVectors bool) result.Result {
	var content string
	var vector []float32
	tags := make(map[string]string)
	numerics := make(map[string]float64)

	for k, v := range entry.Fields {
		switch {
		case k == "__content":
			content = v
		case k == "__vector":
			if includeVectors {
				vector = bytesToVector(v)
			}
		case k == "__vector_score":
			// handled by db layer via entry.Score
		case strings.HasPrefix(k, numericPrefix):
			name := k[len(numericPrefix):]
			if f, err := strconv.ParseFloat(v, 64); err == nil {
				numerics[name] = f
			}
		case strings.HasPrefix(k, "__"):
			// skip other reserved fields
		default:
			tags[k] = v
		}
	}

	tags, numerics = projectedMetadata(entry.Fields, tags, numerics)
	return result.New(docID, entry.Score, content, tags, numerics, vector)
}

// parseBM25EntryFields parses a BM25 entry from flat hash fields.
func parseBM25EntryFields(docID string, score float64, entry db.SearchEntry) result.Result {
	var content string
	tags := make(map[string]string)
	numerics := make(map[string]float64)

	for k, v := range entry.Fields {
		switch {
		case k == "__content":
			content = v
		case k == "__vector":
			// skip vector in BM25
		case strings.HasPrefix(k, numericPrefix):
			name := k[len(numericPrefix):]
			if f, err := strconv.ParseFloat(v, 64); err == nil {
				numerics[name] = f
			}
		case strings.HasPrefix(k, "__"):
			// skip reserved fields
		default:
			tags[k] = v
		}
	}

	tags, numerics = projectedMetadata(entry.Fields, tags, numerics)
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

func (r *Repo) filterCommitted(ctx context.Context, collection string, found *db.SearchResult) error {
	if found == nil {
		return nil
	}
	keys := make([]string, 0, len(found.Entries))
	positions := map[string]int{}
	for _, entry := range found.Entries {
		id := entry.Fields["__parent_id"]
		if id == "" {
			continue
		}
		if _, exists := positions[id]; !exists {
			positions[id] = len(keys)
			keys = append(keys, domain.GenerationDocumentPrefix(collection, domain.CollectionGeneration(ctx))+id)
		}
	}
	if len(keys) == 0 {
		return nil
	}
	versions, err := r.store.HGetFieldMulti(ctx, keys, "__chunk_version")
	if err != nil {
		return fmt.Errorf("read committed document versions: %w", err)
	}
	if len(versions) != len(keys) {
		return fmt.Errorf("document version count mismatch")
	}
	visible := found.Entries[:0]
	for _, entry := range found.Entries {
		id := entry.Fields["__parent_id"]
		if id == "" || (entry.Fields["__version"] != "" && versions[positions[id]] == entry.Fields["__version"]) {
			visible = append(visible, entry)
		}
	}
	found.Entries = visible
	found.Total = len(visible)
	return nil
}

func projectedMetadata(
	fields map[string]string,
	tags map[string]string,
	numerics map[string]float64,
) (projectedTags map[string]string, projectedNumerics map[string]float64) {
	if fields["__metadata"] == "" {
		return tags, numerics
	}
	var metadata db.RecordMetadata
	if json.Unmarshal([]byte(fields["__metadata"]), &metadata) != nil {
		return tags, numerics
	}
	return metadata.Tags, metadata.Numerics
}
