package search

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/kailas-cloud/vecdex/internal/db"
	"github.com/kailas-cloud/vecdex/internal/domain"
	domdoc "github.com/kailas-cloud/vecdex/internal/domain/document"
	"github.com/kailas-cloud/vecdex/internal/domain/metadata"
	"github.com/kailas-cloud/vecdex/internal/domain/search/filter"
	"github.com/kailas-cloud/vecdex/internal/domain/search/result"
	"github.com/kailas-cloud/vecdex/internal/domain/storage"
)

// store is the consumer interface for search operations (ISP).
type store interface {
	HGetFieldsMulti(ctx context.Context, keys, fields []string) ([][]string, error)
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
	layout, err := storage.NewCollection(collectionName, domain.CollectionGeneration(ctx))
	if err != nil {
		return nil, fmt.Errorf("search layout: %w", err)
	}
	indexName := layout.Index()

	q := &db.KNNQuery{
		NotExpiredAt:    time.Now().UnixMilli(),
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
	layout, err := storage.NewCollection(collectionName, domain.CollectionGeneration(ctx))
	if err != nil {
		return nil, fmt.Errorf("search layout: %w", err)
	}
	indexName := layout.Index()

	q := &db.TextQuery{
		NotExpiredAt: time.Now().UnixMilli(),
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
		res, err := parseEntryFields(docID, entry, includeVectors)
		if err != nil {
			return nil, err
		}
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
		res, err := parseBM25EntryFields(docID, entry.Score, entry)
		if err != nil {
			return nil, err
		}
		results = append(results, res)
	}

	return results, nil
}

// numericPrefix disambiguates numeric fields from tags in HASH storage.
const numericPrefix = "__n:"

// parseEntryFields parses a KNN entry from flat hash fields.
func parseEntryFields(docID string, entry db.SearchEntry, includeVectors bool) (result.Result, error) {
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
	return withSourceMetadata(result.New(docID, entry.Score, content, tags, numerics, vector), entry.Fields)
}

// parseBM25EntryFields parses a BM25 entry from flat hash fields.
func parseBM25EntryFields(docID string, score float64, entry db.SearchEntry) (result.Result, error) {
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
	return withSourceMetadata(result.New(docID, score, content, tags, numerics, nil), entry.Fields)
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
	layout, err := storage.NewCollection(collection, domain.CollectionGeneration(ctx))
	if err != nil {
		return fmt.Errorf("search layout: %w", err)
	}
	keys, positions := committedParentKeys(layout, found.Entries)
	if len(keys) == 0 {
		return nil
	}
	rows, err := r.store.HGetFieldsMulti(ctx, keys,
		[]string{"__chunk_version", "__source_metadata", "__source_version", "__expires_at"})
	if err != nil {
		return fmt.Errorf("read committed source projection: %w", err)
	}
	if len(rows) != len(keys) {
		return fmt.Errorf("document projection count mismatch")
	}
	versions := make([]string, len(rows))
	for i, row := range rows {
		if len(row) != 4 {
			return fmt.Errorf("document projection width mismatch")
		}
		expiresAt, err := domdoc.ParseExpiresAt(row[3])
		if err != nil {
			return fmt.Errorf("read committed expiration: %w", err)
		}
		if !domdoc.Expired(expiresAt, time.Now().UnixMilli()) {
			versions[i] = row[0]
		}
	}
	keepCommittedEntries(found, positions, versions)
	for i := range found.Entries {
		entry := &found.Entries[i]
		if id := entry.Fields["__parent_id"]; id != "" {
			if rows[positions[id]][2] == "" {
				return fmt.Errorf("committed source %q has no version", id)
			}
			entry.Fields["__source_metadata"] = rows[positions[id]][1]
			entry.Fields["__source_version"] = rows[positions[id]][2]
		}
	}
	return nil
}

func withSourceMetadata(value result.Result, fields map[string]string) (result.Result, error) {
	if fields["__source_metadata"] == "" {
		return value.WithSource(fields["__source_version"], metadata.Object{}), nil
	}
	object, err := metadata.Parse([]byte(fields["__source_metadata"]))
	if err != nil {
		return result.Result{}, fmt.Errorf("decode search source metadata: %w", err)
	}
	return value.WithSource(fields["__source_version"], object), nil
}

func committedParentKeys(
	layout storage.Collection, entries []db.SearchEntry,
) (parentKeys []string, parentPositions map[string]int) {
	keys := make([]string, 0, len(entries))
	positions := map[string]int{}
	for _, entry := range entries {
		id := entry.Fields["__parent_id"]
		if id == "" {
			continue
		}
		if _, exists := positions[id]; !exists {
			positions[id] = len(keys)
			keys = append(keys, layout.Document(id))
		}
	}
	return keys, positions
}

func keepCommittedEntries(found *db.SearchResult, positions map[string]int, versions []string) {
	visible := found.Entries[:0]
	for _, entry := range found.Entries {
		id := entry.Fields["__parent_id"]
		if id == "" || (entry.Fields["__version"] != "" && versions[positions[id]] == entry.Fields["__version"]) {
			visible = append(visible, entry)
		}
	}
	found.Entries = visible
	found.Total = len(visible)
}

func projectedMetadata(
	fields map[string]string,
	tags map[string]string,
	numerics map[string]float64,
) (projectedTags map[string]string, projectedNumerics map[string]float64) {
	if fields["__metadata"] == "" {
		return tags, numerics
	}
	var projection db.RecordMetadata
	if json.Unmarshal([]byte(fields["__metadata"]), &projection) != nil {
		return tags, numerics
	}
	return projection.Tags, projection.Numerics
}
