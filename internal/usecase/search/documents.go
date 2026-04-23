package search

import (
	"sort"

	domcol "github.com/kailas-cloud/vecdex/internal/domain/collection"
	"github.com/kailas-cloud/vecdex/internal/domain/search/result"
)

type docCandidate struct {
	id        string
	score     float64
	rank      int
	chunkRank float64
	res       result.Result
}

func aggregateToDocuments(results []result.Result, topK int) []result.Result {
	if len(results) == 0 {
		return nil
	}

	bestByDoc := make(map[string]docCandidate, len(results))
	for idx, r := range results {
		docID := documentID(&r)
		candidate := docCandidate{
			id:        docID,
			score:     r.Score(),
			rank:      idx,
			chunkRank: chunkIndex(&r),
			res:       r,
		}

		current, ok := bestByDoc[docID]
		if !ok || betterDocumentCandidate(candidate, current) {
			bestByDoc[docID] = candidate
		}
	}

	candidates := make([]docCandidate, 0, len(bestByDoc))
	for _, candidate := range bestByDoc {
		candidates = append(candidates, candidate)
	}

	sort.Slice(candidates, func(i, j int) bool {
		return betterDocumentCandidate(candidates[i], candidates[j])
	})

	if len(candidates) > topK {
		candidates = candidates[:topK]
	}

	docResults := make([]result.Result, len(candidates))
	for i, candidate := range candidates {
		docResults[i] = result.New(
			candidate.id,
			candidate.score,
			candidate.res.Content(),
			candidate.res.Tags(),
			candidate.res.Numerics(),
			candidate.res.Vector(),
		)
	}

	return docResults
}

func betterDocumentCandidate(candidate, current docCandidate) bool {
	if candidate.score != current.score {
		return candidate.score > current.score
	}
	if candidate.rank != current.rank {
		return candidate.rank < current.rank
	}
	if candidate.chunkRank != current.chunkRank {
		return candidate.chunkRank < current.chunkRank
	}
	return candidate.id < current.id
}

func documentID(r *result.Result) string {
	if r == nil {
		return ""
	}
	if parentDocID, ok := r.Tags()[domcol.SystemParentDocID]; ok && parentDocID != "" {
		return parentDocID
	}
	return r.ID()
}

func chunkIndex(r *result.Result) float64 {
	if r == nil {
		return 0
	}
	if idx, ok := r.Numerics()[domcol.SystemChunkIndex]; ok {
		return idx
	}
	return 0
}
