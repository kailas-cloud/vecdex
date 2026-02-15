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
func fuseRRF(knn, bm25 []result.Result, topK int) []result.Result {
	type scored struct {
		res   result.Result
		score float64
		inKNN bool
	}

	merged := make(map[string]*scored)

	for rank, r := range knn {
		s := 1.0 / float64(rrfK+rank+1)
		merged[r.ID()] = &scored{res: r, score: s, inKNN: true}
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
	scores := make(map[string]float64, len(merged))
	for id, s := range merged {
		scores[id] = s.score
		// Rebuild result with fused RRF score
		results = append(results, result.New(
			s.res.ID(), s.score, s.res.Content(),
			s.res.Tags(), s.res.Numerics(), s.res.Vector(),
		))
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].Score() > results[j].Score()
	})

	if len(results) > topK {
		results = results[:topK]
	}

	return results
}
