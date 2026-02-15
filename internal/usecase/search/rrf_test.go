package search

import (
	"math"
	"testing"

	"github.com/kailas-cloud/vecdex/internal/domain/search/result"
)

func makeResult(id string) result.Result {
	return result.New(id, 0, "content-"+id, nil, nil, nil)
}

func makeResultWithVector(id string, vec []float32) result.Result {
	return result.New(id, 0, "content-"+id, nil, nil, vec)
}

func TestFuseRRF_DisjointLists(t *testing.T) {
	knn := []result.Result{makeResult("a"), makeResult("b")}
	bm25 := []result.Result{makeResult("c"), makeResult("d")}

	results := fuseRRF(knn, bm25, 10)
	if len(results) != 4 {
		t.Fatalf("expected 4 results, got %d", len(results))
	}

	// Rank 1 in both lists have equal scores
	ids := make(map[string]bool)
	for _, r := range results {
		ids[r.ID()] = true
	}
	for _, id := range []string{"a", "b", "c", "d"} {
		if !ids[id] {
			t.Errorf("missing result %s", id)
		}
	}
}

func TestFuseRRF_OverlappingLists(t *testing.T) {
	knn := []result.Result{makeResult("a"), makeResult("b"), makeResult("c")}
	bm25 := []result.Result{makeResult("b"), makeResult("d"), makeResult("a")}

	results := fuseRRF(knn, bm25, 10)
	if len(results) != 4 {
		t.Fatalf("expected 4 results, got %d", len(results))
	}

	// "a" and "b" appear in both lists, so they get higher RRF scores
	// "a": rank 0 in KNN (1/61) + rank 2 in BM25 (1/63)
	// "b": rank 1 in KNN (1/62) + rank 0 in BM25 (1/61)
	if results[0].ID() != "b" && results[0].ID() != "a" {
		t.Errorf("expected 'a' or 'b' first, got %s", results[0].ID())
	}

	// Overlap docs should have higher scores than single-list docs
	overlapScore := results[0].Score()
	var singleScore float64
	for _, r := range results {
		if r.ID() == "c" || r.ID() == "d" {
			singleScore = r.Score()
			break
		}
	}
	if overlapScore <= singleScore {
		t.Errorf("overlap score %f should be > single score %f", overlapScore, singleScore)
	}
}

func TestFuseRRF_EmptyInputs(t *testing.T) {
	t.Run("both empty", func(t *testing.T) {
		results := fuseRRF(nil, nil, 10)
		if len(results) != 0 {
			t.Fatalf("expected 0 results, got %d", len(results))
		}
	})

	t.Run("knn empty", func(t *testing.T) {
		bm25 := []result.Result{makeResult("a")}
		results := fuseRRF(nil, bm25, 10)
		if len(results) != 1 {
			t.Fatalf("expected 1 result, got %d", len(results))
		}
	})

	t.Run("bm25 empty", func(t *testing.T) {
		knn := []result.Result{makeResult("a")}
		results := fuseRRF(knn, nil, 10)
		if len(results) != 1 {
			t.Fatalf("expected 1 result, got %d", len(results))
		}
	})
}

func TestFuseRRF_TopKLimiting(t *testing.T) {
	knn := []result.Result{makeResult("a"), makeResult("b"), makeResult("c")}
	bm25 := []result.Result{makeResult("d"), makeResult("e"), makeResult("f")}

	results := fuseRRF(knn, bm25, 3)
	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}
}

func TestFuseRRF_SortedByScore(t *testing.T) {
	knn := []result.Result{makeResult("a"), makeResult("b")}
	bm25 := []result.Result{makeResult("c"), makeResult("d")}

	results := fuseRRF(knn, bm25, 10)
	for i := 1; i < len(results); i++ {
		if results[i].Score() > results[i-1].Score() {
			t.Errorf("results not sorted: %f > %f at index %d",
				results[i].Score(), results[i-1].Score(), i)
		}
	}
}

func TestFuseRRF_PreservesKNNVector(t *testing.T) {
	vec := []float32{0.1, 0.2, 0.3}
	knn := []result.Result{makeResultWithVector("a", vec)}
	bm25 := []result.Result{makeResult("a")} // same doc, no vector

	results := fuseRRF(knn, bm25, 10)
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if len(results[0].Vector()) != 3 {
		t.Fatalf("expected vector len 3, got %d", len(results[0].Vector()))
	}
}

func TestFuseRRF_ScoreFormula(t *testing.T) {
	knn := []result.Result{makeResult("a")}
	bm25 := []result.Result{makeResult("a")}

	results := fuseRRF(knn, bm25, 10)
	// "a" is rank 0 in both: 1/(60+1) + 1/(60+1) = 2/61
	expected := 2.0 / 61.0
	if math.Abs(results[0].Score()-expected) > 1e-10 {
		t.Errorf("expected score %f, got %f", expected, results[0].Score())
	}
}
