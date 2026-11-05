package search

import (
	"bytes"
	"testing"

	"github.com/kailas-cloud/vecdex/internal/domain/metadata"
	"github.com/kailas-cloud/vecdex/internal/domain/search/result"
)

func TestRankingAndSourceAggregationPreserveJSONMetadata(t *testing.T) {
	value, err := metadata.Parse([]byte(`{"nested":{"value":9007199254740993}}`))
	if err != nil {
		t.Fatal(err)
	}
	knn := []result.Result{
		result.New("source", .9, "best snippet", nil, nil, nil).WithSource("source-version", value),
	}
	keyword := []result.Result{
		result.New("source", .8, "other snippet", nil, nil, nil).WithSource("source-version", value),
	}
	for _, hits := range [][]result.Result{aggregateToDocuments(knn, 10), fuseRRF(knn, keyword, 10)} {
		if len(hits) != 1 || !bytes.Equal(hits[0].Metadata().JSON(), value.JSON()) || hits[0].Version() != "source-version" {
			t.Fatal("ranking dropped the source version or metadata")
		}
	}
}

func TestMetadataCannotChangeSourceIdentity(t *testing.T) {
	hits := []result.Result{
		result.New("source-a", .9, "first", map[string]string{"parent_doc_id": "same-group"},
			map[string]float64{"chunk_index": 1}, nil),
		result.New("source-b", .8, "second", map[string]string{"parent_doc_id": "same-group"},
			map[string]float64{"chunk_index": 2}, nil),
	}
	for _, ranked := range [][]result.Result{aggregateToDocuments(hits, 10), fuseRRF(hits, hits, 10)} {
		if len(ranked) != 2 || ranked[0].ID() != "source-a" || ranked[1].ID() != "source-b" {
			t.Fatalf("metadata changed source identities: %+v", ranked)
		}
	}
}
