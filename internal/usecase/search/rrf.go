package search

import (
	"sort"

	"github.com/kailas-cloud/vecdex/internal/domain/search/result"
)

// rrfK is the Reciprocal Rank Fusion constant (standard value from Cormack et al. 2009).
const rrfK = 60

// fuseRRF merges KNN and BM25 results via Reciprocal Rank Fusion.
// score(d) = sum of 1/(k + rank_i(d)) for each ranking where d appears.
// When a document appears in both lists, the KNN result is kept (it may contain the vector).
// limit<=0 keeps the full merged list.
func fuseRRF(knn, bm25 []result.Result, limit int) []result.Result {
	type scored struct {
		res   result.Result
		score float64
	}

	// Rank documents, not fragments. Duplicate fragments must neither overwrite
	// the best snippet nor contribute multiple votes from the same search mode.
	knn = aggregateToDocuments(knn, len(knn))
	bm25 = aggregateToDocuments(bm25, len(bm25))
	merged := make(map[string]*scored)

	for rank, r := range knn {
		s := 1.0 / float64(rrfK+rank+1)
		merged[r.ID()] = &scored{res: r, score: s}
	}

	for rank, r := range bm25 {
		s := 1.0 / float64(rrfK+rank+1)
		if existing, ok := merged[r.ID()]; ok {
			existing.score += s
			// KNN result takes priority (contains vector)
		} else {
			merged[r.ID()] = &scored{res: r, score: s}
		}
	}

	results := make([]result.Result, 0, len(merged))
	for _, s := range merged {
		// Rebuild result with fused RRF score
		results = append(results, result.New(
			s.res.ID(), s.score, s.res.Content(),
			s.res.Tags(), s.res.Numerics(), s.res.Vector(),
		).WithSourceOf(&s.res))
	}

	sort.Slice(results, func(i, j int) bool {
		if results[i].Score() != results[j].Score() {
			return results[i].Score() > results[j].Score()
		}
		return results[i].ID() < results[j].ID()
	})

	if limit > 0 && len(results) > limit {
		results = results[:limit]
	}

	return results
}
