package vecdex

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	dombatch "github.com/kailas-cloud/vecdex/internal/domain/batch"
	domdoc "github.com/kailas-cloud/vecdex/internal/domain/document"
)

const testChunkerTokenizerJSON = `{
  "version":"1.0",
  "added_tokens":[
    {"id":0,"special":true,"content":"[PAD]","single_word":false,"lstrip":false,"rstrip":false,"normalized":false},
    {"id":1,"special":true,"content":"[UNK]","single_word":false,"lstrip":false,"rstrip":false,"normalized":false},
    {"id":2,"special":true,"content":"[CLS]","single_word":false,"lstrip":false,"rstrip":false,"normalized":false},
    {"id":3,"special":true,"content":"[SEP]","single_word":false,"lstrip":false,"rstrip":false,"normalized":false},
    {"id":4,"special":true,"content":"[MASK]","single_word":false,"lstrip":false,"rstrip":false,"normalized":false}
  ],
  "normalizer":{
    "type":"BertNormalizer",
    "clean_text":true,
    "handle_chinese_chars":true,
    "strip_accents":null,
    "lowercase":true
  },
  "pre_tokenizer":{"type":"BertPreTokenizer"},
  "post_processor":{
    "type":"TemplateProcessing",
    "single":[
      {"SpecialToken":{"id":"[CLS]","type_id":0}},
      {"Sequence":{"id":"A","type_id":0}},
      {"SpecialToken":{"id":"[SEP]","type_id":0}}
    ],
    "pair":[
      {"SpecialToken":{"id":"[CLS]","type_id":0}},
      {"Sequence":{"id":"A","type_id":0}},
      {"SpecialToken":{"id":"[SEP]","type_id":0}},
      {"Sequence":{"id":"B","type_id":1}},
      {"SpecialToken":{"id":"[SEP]","type_id":1}}
    ],
    "special_tokens":{
      "[CLS]":{"id":"[CLS]","ids":[2],"tokens":["[CLS]"]},
      "[SEP]":{"id":"[SEP]","ids":[3],"tokens":["[SEP]"]}
    }
  },
  "decoder":{"type":"WordPiece","prefix":"##","cleanup":true},
  "model":{
    "type":"WordPiece",
    "unk_token":"[UNK]",
    "continuing_subword_prefix":"##",
    "max_input_chars_per_word":100,
    "vocab":{
      "[PAD]":0,
      "[UNK]":1,
      "[CLS]":2,
      "[SEP]":3,
      "[MASK]":4,
      "alpha":5,
      "beta":6,
      "gamma":7,
      "delta":8,
      "epsilon":9,
      "zeta":10,
      "eta":11,
      "theta":12,
      "iota":13,
      "kappa":14,
      "lambda":15,
      "mu":16
    }
  }
}`

type staticChunker struct {
	chunks map[string][]TextChunk
}

func (c staticChunker) Chunk(text string) ([]TextChunk, error) {
	if chunks, ok := c.chunks[text]; ok {
		return append([]TextChunk(nil), chunks...), nil
	}
	return []TextChunk{{Content: text, Tokens: 1}}, nil
}

func TestONNXChunkerSentenceOverlap(t *testing.T) {
	modelDir := writeChunkerModelDir(t)
	chunker, err := NewONNXChunker(ONNXChunkerConfig{
		ModelDir:       modelDir,
		WindowTokens:   15,
		OverlapPercent: 0.25,
	})
	if err != nil {
		t.Fatalf("NewONNXChunker() error = %v", err)
	}

	chunks, err := chunker.Chunk("Alpha beta gamma. Delta epsilon zeta eta theta iota. Kappa lambda mu.")
	if err != nil {
		t.Fatalf("Chunk() error = %v", err)
	}

	if len(chunks) != 2 {
		t.Fatalf("len(chunks) = %d, want 2", len(chunks))
	}
	if got := chunks[0].Content; got != "Alpha beta gamma. Delta epsilon zeta eta theta iota." {
		t.Fatalf("chunks[0] = %q", got)
	}
	if got := chunks[1].Content; got != "Delta epsilon zeta eta theta iota. Kappa lambda mu." {
		t.Fatalf("chunks[1] = %q", got)
	}
}

func TestONNXChunkerRejectsOversizedSentence(t *testing.T) {
	modelDir := writeChunkerModelDir(t)
	chunker, err := NewONNXChunker(ONNXChunkerConfig{
		ModelDir:       modelDir,
		WindowTokens:   4,
		OverlapPercent: 0.10,
	})
	if err != nil {
		t.Fatalf("NewONNXChunker() error = %v", err)
	}

	if _, err := chunker.Chunk("Alpha beta gamma."); err == nil {
		t.Fatal("Chunk() expected oversized sentence error")
	}
}

func TestTextIngestorBatchesInParallel(t *testing.T) {
	var (
		mu            sync.Mutex
		maxConcurrent int
		inFlight      int
		seenIDs       []string
		parentTags    = map[string]string{}
		chunkIndexes  = map[string]float64{}
	)
	batchUC := &mockBatchUC{
		upsertFn: func(_ context.Context, _ string, docs []domdoc.Document) []dombatch.Result {
			mu.Lock()
			inFlight++
			if inFlight > maxConcurrent {
				maxConcurrent = inFlight
			}
			for _, doc := range docs {
				seenIDs = append(seenIDs, doc.ID())
				parentTags[doc.ID()] = doc.Tags()[ParentDocIDTag]
				chunkIndexes[doc.ID()] = doc.Numerics()[ChunkIndexNumeric]
			}
			mu.Unlock()

			time.Sleep(20 * time.Millisecond)

			mu.Lock()
			inFlight--
			mu.Unlock()

			results := make([]dombatch.Result, len(docs))
			for i, doc := range docs {
				results[i] = dombatch.NewOK(doc.ID())
			}
			return results
		},
	}

	ingestor := NewTextIngestor(newDocSvc(nil, batchUC), staticChunker{
		chunks: map[string][]TextChunk{
			"doc-a": {
				{Content: "doc-a chunk 1", Tokens: 5},
				{Content: "doc-a chunk 2", Tokens: 5},
			},
			"doc-b": {
				{Content: "doc-b chunk 1", Tokens: 5},
				{Content: "doc-b chunk 2", Tokens: 5},
			},
			"doc-c": {
				{Content: "doc-c chunk 1", Tokens: 5},
				{Content: "doc-c chunk 2", Tokens: 5},
			},
			"doc-d": {
				{Content: "doc-d chunk 1", Tokens: 5},
				{Content: "doc-d chunk 2", Tokens: 5},
			},
		},
	}, &IngestorConfig{
		BatchSize:   2,
		Parallelism: 4,
	})

	start := time.Now()
	resp, err := ingestor.Upsert(context.Background(), []Document{
		{ID: "a", Content: "doc-a"},
		{ID: "b", Content: "doc-b"},
		{ID: "c", Content: "doc-c"},
		{ID: "d", Content: "doc-d"},
	})
	if err != nil {
		t.Fatalf("Upsert() error = %v", err)
	}
	elapsed := time.Since(start)

	if resp.Succeeded != 8 || resp.Failed != 0 {
		t.Fatalf("response = %+v, want 8 succeeded", resp)
	}
	if maxConcurrent < 2 {
		t.Fatalf("maxConcurrent = %d, want >= 2", maxConcurrent)
	}
	if elapsed >= 60*time.Millisecond {
		t.Fatalf("elapsed = %s, want < 60ms", elapsed)
	}

	wantIDs := []string{
		"a-chunk-0001", "a-chunk-0002",
		"b-chunk-0001", "b-chunk-0002",
		"c-chunk-0001", "c-chunk-0002",
		"d-chunk-0001", "d-chunk-0002",
	}
	if len(seenIDs) != len(wantIDs) {
		t.Fatalf("len(seenIDs) = %d, want %d", len(seenIDs), len(wantIDs))
	}
	for _, want := range wantIDs {
		if !containsString(seenIDs, want) {
			t.Fatalf("missing chunk ID %q in %v", want, seenIDs)
		}
	}
	for _, id := range wantIDs {
		parent := string(id[0])
		if got := parentTags[id]; got != parent {
			t.Fatalf("parent_doc_id[%q] = %q, want %q", id, got, parent)
		}
		if got := chunkIndexes[id]; got != chunkIndexFromID(id) {
			t.Fatalf("chunk_index[%q] = %v, want %v", id, got, chunkIndexFromID(id))
		}
	}
}

func TestTextIngestorKeepsSingleChunkDocumentID(t *testing.T) {
	var seenIDs []string
	var parentTag string
	var chunkIndex float64
	batchUC := &mockBatchUC{
		upsertFn: func(_ context.Context, _ string, docs []domdoc.Document) []dombatch.Result {
			results := make([]dombatch.Result, len(docs))
			for i, doc := range docs {
				seenIDs = append(seenIDs, doc.ID())
				parentTag = doc.Tags()[ParentDocIDTag]
				chunkIndex = doc.Numerics()[ChunkIndexNumeric]
				results[i] = dombatch.NewOK(doc.ID())
			}
			return results
		},
	}

	ingestor := NewTextIngestor(newDocSvc(nil, batchUC), staticChunker{}, nil)
	_, err := ingestor.Upsert(context.Background(), []Document{{ID: "raw-id", Content: "single chunk"}})
	if err != nil {
		t.Fatalf("Upsert() error = %v", err)
	}
	if len(seenIDs) != 1 || seenIDs[0] != "raw-id" {
		t.Fatalf("seenIDs = %v, want [raw-id]", seenIDs)
	}
	if parentTag != "raw-id" {
		t.Fatalf("parentTag = %q, want raw-id", parentTag)
	}
	if chunkIndex != 1 {
		t.Fatalf("chunkIndex = %v, want 1", chunkIndex)
	}
}

func writeChunkerModelDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "tokenizer.json"), []byte(testChunkerTokenizerJSON), 0o600); err != nil {
		t.Fatalf("write tokenizer: %v", err)
	}
	return dir
}

func containsString(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}

func chunkIndexFromID(id string) float64 {
	switch {
	case strings.HasSuffix(id, "0001"):
		return 1
	case strings.HasSuffix(id, "0002"):
		return 2
	default:
		return 0
	}
}
