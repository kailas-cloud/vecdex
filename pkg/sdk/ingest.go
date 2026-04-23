package vecdex

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"unicode"

	hftokenizer "github.com/sugarme/tokenizer"
	"github.com/sugarme/tokenizer/pretrained"
)

const (
	// DefaultChunkWindowTokens is the target ONNX text window size used for chunking.
	DefaultChunkWindowTokens = 256
	// DefaultChunkOverlapPercent is the sentence-preserving overlap between chunks.
	DefaultChunkOverlapPercent = 0.10
	// DefaultIngestBatchSize is the number of chunk documents per batch upsert.
	DefaultIngestBatchSize = 100
	// DefaultIngestParallelism is the number of concurrent batch upserts.
	DefaultIngestParallelism = 10
	// ParentDocIDTag links a chunk back to its source document.
	ParentDocIDTag = "parent_doc_id"
	// ChunkIndexNumeric stores the 1-based chunk order within a parent document.
	ChunkIndexNumeric = "chunk_index"
)

// TextChunk is a sentence-aligned text span produced by a chunker.
type TextChunk struct {
	Content string
	Tokens  int
}

// Chunker splits long text into model-sized chunks.
type Chunker interface {
	Chunk(text string) ([]TextChunk, error)
}

// ONNXChunkerConfig configures the local tokenizer-backed text chunker.
type ONNXChunkerConfig struct {
	ModelDir       string
	WindowTokens   int
	OverlapPercent float64
}

type chunkTokenizer interface {
	EncodeBatch(inputs []hftokenizer.EncodeInput, addSpecialTokens bool) ([]hftokenizer.Encoding, error)
}

// ONNXChunker uses the local tokenizer.json so chunk sizing matches ONNX ingest.
type ONNXChunker struct {
	tokenizer     chunkTokenizer
	maxTokens     int
	overlapTokens int
}

// NewONNXChunker creates a sentence-aware chunker for the given local model dir.
func NewONNXChunker(cfg ONNXChunkerConfig) (*ONNXChunker, error) {
	if cfg.ModelDir == "" {
		return nil, fmt.Errorf("onnx chunker: model dir is required")
	}
	maxTokens := cfg.WindowTokens
	if maxTokens <= 0 {
		maxTokens = DefaultChunkWindowTokens
	}
	overlapPercent := cfg.OverlapPercent
	if overlapPercent <= 0 {
		overlapPercent = DefaultChunkOverlapPercent
	}
	if overlapPercent >= 1 {
		return nil, fmt.Errorf("onnx chunker: overlap percent must be < 1, got %.2f", overlapPercent)
	}

	tk, err := pretrained.FromFile(filepath.Join(cfg.ModelDir, "tokenizer.json"))
	if err != nil {
		return nil, fmt.Errorf("onnx chunker: load tokenizer: %w", err)
	}

	overlapTokens := int(float64(maxTokens)*overlapPercent + 0.5)
	if overlapTokens >= maxTokens {
		overlapTokens = maxTokens - 1
	}
	if overlapTokens < 0 {
		overlapTokens = 0
	}

	return &ONNXChunker{
		tokenizer:     tk,
		maxTokens:     maxTokens,
		overlapTokens: overlapTokens,
	}, nil
}

// Chunk splits text into sentence-aligned chunks with token overlap.
func (c *ONNXChunker) Chunk(text string) ([]TextChunk, error) {
	sentences := splitSentences(text)
	if len(sentences) == 0 {
		return nil, nil
	}

	tokenCounts, err := c.countTokens(sentences)
	if err != nil {
		return nil, err
	}

	chunks := make([]TextChunk, 0, len(sentences))
	for start := 0; start < len(sentences); {
		end := start
		totalTokens := 0
		for end < len(sentences) && totalTokens+tokenCounts[end] <= c.maxTokens {
			totalTokens += tokenCounts[end]
			end++
		}
		if end == start {
			return nil, fmt.Errorf(
				"onnx chunker: sentence exceeds %d-token window",
				c.maxTokens,
			)
		}

		chunks = append(chunks, TextChunk{
			Content: strings.Join(sentences[start:end], " "),
			Tokens:  totalTokens,
		})
		if end == len(sentences) {
			break
		}

		nextStart := end
		if c.overlapTokens > 0 {
			overlap := 0
			for i := end - 1; i > start; i-- {
				overlap += tokenCounts[i]
				nextStart = i
				if overlap >= c.overlapTokens {
					break
				}
			}
		}
		start = nextStart
	}

	return chunks, nil
}

func (c *ONNXChunker) countTokens(sentences []string) ([]int, error) {
	inputs := make([]hftokenizer.EncodeInput, len(sentences))
	for i, sentence := range sentences {
		inputs[i] = hftokenizer.NewSingleEncodeInput(hftokenizer.NewInputSequence(sentence))
	}

	encodings, err := c.tokenizer.EncodeBatch(inputs, true)
	if err != nil {
		return nil, fmt.Errorf("onnx chunker: encode sentences: %w", err)
	}

	counts := make([]int, len(encodings))
	for i := range encodings {
		for _, mask := range encodings[i].AttentionMask {
			counts[i] += mask
		}
	}
	return counts, nil
}

func splitSentences(text string) []string {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}

	runes := []rune(text)
	sentences := make([]string, 0, 8)
	start := 0
	for i := 0; i < len(runes); i++ {
		if handled, nextStart, nextIndex := consumeParagraphBreak(runes, start, i, &sentences); handled {
			start = nextStart
			i = nextIndex
			continue
		}
		boundaryEnd, ok := sentenceBoundaryEnd(runes, i)
		if !ok {
			continue
		}
		appendSentence(&sentences, runes[start:boundaryEnd])
		start = boundaryEnd
	}

	appendSentence(&sentences, runes[start:])
	return sentences
}

func consumeParagraphBreak(
	runes []rune,
	start int,
	index int,
	sentences *[]string,
) (handled bool, nextStart, nextIndex int) {
	if runes[index] != '\n' || index+1 >= len(runes) || runes[index+1] != '\n' {
		return false, start, index
	}
	appendSentence(sentences, runes[start:index])
	return true, index + 2, index + 1
}

func sentenceBoundaryEnd(runes []rune, index int) (int, bool) {
	if !isSentenceTerminator(runes[index]) {
		return 0, false
	}
	end := skipClosingPunctuation(runes, index+1)
	if !isBoundaryWhitespace(runes, end) {
		return 0, false
	}
	return end, true
}

func skipClosingPunctuation(runes []rune, index int) int {
	for index < len(runes) && isClosingPunctuation(runes[index]) {
		index++
	}
	return index
}

func isBoundaryWhitespace(runes []rune, index int) bool {
	if index >= len(runes) {
		return true
	}
	return unicode.IsSpace(runes[index]) || runes[index] == '\n'
}

func appendSentence(sentences *[]string, runes []rune) {
	if sentence := normalizeSentenceRunes(runes); sentence != "" {
		*sentences = append(*sentences, sentence)
	}
}

func normalizeSentenceRunes(runes []rune) string {
	return strings.Join(strings.Fields(string(runes)), " ")
}

func isSentenceTerminator(r rune) bool {
	switch r {
	case '.', '!', '?':
		return true
	default:
		return false
	}
}

func isClosingPunctuation(r rune) bool {
	switch r {
	case '"', '\'', ')', ']', '}':
		return true
	default:
		return false
	}
}

// IngestorConfig controls chunked batch ingestion.
type IngestorConfig struct {
	BatchSize   int
	Parallelism int
}

// TextIngestor chunks long text documents and uploads them in parallel batches.
type TextIngestor struct {
	docs        *DocumentService
	chunker     Chunker
	batchSize   int
	parallelism int
}

// NewTextIngestor creates a chunking ingestor over the SDK document service.
func NewTextIngestor(
	docs *DocumentService,
	chunker Chunker,
	cfg *IngestorConfig,
) *TextIngestor {
	batchSize := DefaultIngestBatchSize
	parallelism := DefaultIngestParallelism
	if cfg != nil {
		if cfg.BatchSize > 0 {
			batchSize = cfg.BatchSize
		}
		if cfg.Parallelism > 0 {
			parallelism = cfg.Parallelism
		}
	}
	return &TextIngestor{
		docs:        docs,
		chunker:     chunker,
		batchSize:   batchSize,
		parallelism: parallelism,
	}
}

// Upsert chunk-splits the provided documents and uploads them in parallel batches.
func (i *TextIngestor) Upsert(
	ctx context.Context,
	docs []Document,
) (BatchResponse, error) {
	chunkedDocs, err := i.chunkDocuments(docs)
	if err != nil {
		return BatchResponse{}, err
	}
	if len(chunkedDocs) == 0 {
		return BatchResponse{}, nil
	}

	batches := shardDocuments(chunkedDocs, i.batchSize)
	return i.upsertParallel(ctx, batches)
}

type ingestJob struct {
	index int
	docs  []Document
}

type ingestResult struct {
	index int
	resp  BatchResponse
	err   error
}

func (i *TextIngestor) upsertParallel(
	ctx context.Context,
	batches [][]Document,
) (BatchResponse, error) {
	workers := maxInt(1, minInt(i.parallelism, len(batches)))
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)

	jobCh := make(chan ingestJob)
	resultCh := make(chan ingestResult, len(batches))

	var wg sync.WaitGroup
	i.startIngestWorkers(ctx, &wg, workers, jobCh, resultCh, cancel)
	go enqueueIngestJobs(ctx, jobCh, batches)

	responses, err := collectIngestResults(ctx, &wg, resultCh, len(batches))
	if err != nil {
		return BatchResponse{}, err
	}
	return mergeBatchResponses(responses), nil
}

func (i *TextIngestor) startIngestWorkers(
	ctx context.Context,
	wg *sync.WaitGroup,
	workers int,
	jobCh <-chan ingestJob,
	resultCh chan<- ingestResult,
	cancel context.CancelCauseFunc,
) {
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for job := range jobCh {
				resp, err := i.docs.BatchUpsert(ctx, job.docs)
				select {
				case resultCh <- ingestResult{index: job.index, resp: resp, err: err}:
				case <-ctx.Done():
					return
				}
				if err != nil {
					cancel(err)
					return
				}
			}
		}()
	}
}

func enqueueIngestJobs(
	ctx context.Context,
	jobCh chan<- ingestJob,
	batches [][]Document,
) {
	defer close(jobCh)
	for idx, batch := range batches {
		select {
		case jobCh <- ingestJob{index: idx, docs: batch}:
		case <-ctx.Done():
			return
		}
	}
}

func collectIngestResults(
	ctx context.Context,
	wg *sync.WaitGroup,
	resultCh <-chan ingestResult,
	expected int,
) ([]BatchResponse, error) {
	responses := make([]BatchResponse, expected)
	for range expected {
		select {
		case <-ctx.Done():
			wg.Wait()
			return nil, fmt.Errorf("parallel ingest: %w", context.Cause(ctx))
		case res := <-resultCh:
			if res.err != nil {
				wg.Wait()
				return nil, res.err
			}
			responses[res.index] = res.resp
		}
	}
	wg.Wait()
	return responses, nil
}

func (i *TextIngestor) chunkDocuments(docs []Document) ([]Document, error) {
	if i.chunker == nil {
		return addParentTags(docs), nil
	}

	chunked := make([]Document, 0, len(docs))
	for _, doc := range docs {
		docChunks, err := i.chunkDocument(doc)
		if err != nil {
			return nil, err
		}
		chunked = append(chunked, docChunks...)
	}
	return chunked, nil
}

func addParentTags(docs []Document) []Document {
	plain := make([]Document, 0, len(docs))
	for _, doc := range docs {
		plain = append(plain, withParentDocIDTag(doc, doc.ID))
	}
	return plain
}

func (i *TextIngestor) chunkDocument(doc Document) ([]Document, error) {
	parts, err := i.chunker.Chunk(doc.Content)
	if err != nil {
		return nil, fmt.Errorf("chunk document %q: %w", doc.ID, err)
	}
	if len(parts) <= 1 {
		return []Document{withParentDocIDTag(doc, doc.ID)}, nil
	}
	return buildChunkDocuments(doc, parts), nil
}

func buildChunkDocuments(doc Document, parts []TextChunk) []Document {
	chunked := make([]Document, 0, len(parts))
	for idx, part := range parts {
		chunked = append(chunked, newChunkDocument(doc, part, idx+1))
	}
	return chunked
}

func newChunkDocument(doc Document, part TextChunk, chunkIndex int) Document {
	tags := ensureTags(cloneTags(doc.Tags))
	tags[ParentDocIDTag] = doc.ID
	numerics := ensureNumerics(cloneNumerics(doc.Numerics))
	numerics[ChunkIndexNumeric] = float64(chunkIndex)
	return Document{
		ID:       fmt.Sprintf("%s-chunk-%04d", doc.ID, chunkIndex),
		Content:  part.Content,
		Tags:     tags,
		Numerics: numerics,
	}
}

func withParentDocIDTag(doc Document, parentDocID string) Document {
	tags := ensureTags(cloneTags(doc.Tags))
	tags[ParentDocIDTag] = parentDocID
	numerics := ensureNumerics(cloneNumerics(doc.Numerics))
	numerics[ChunkIndexNumeric] = 1
	doc.Tags = tags
	doc.Numerics = numerics
	return doc
}

func ensureTags(tags map[string]string) map[string]string {
	if tags != nil {
		return tags
	}
	return make(map[string]string, 1)
}

func ensureNumerics(numerics map[string]float64) map[string]float64 {
	if numerics != nil {
		return numerics
	}
	return make(map[string]float64, 1)
}

func shardDocuments(docs []Document, size int) [][]Document {
	if size <= 0 {
		size = len(docs)
	}
	batches := make([][]Document, 0, (len(docs)+size-1)/size)
	for start := 0; start < len(docs); start += size {
		end := start + size
		if end > len(docs) {
			end = len(docs)
		}
		batches = append(batches, docs[start:end])
	}
	return batches
}

func mergeBatchResponses(parts []BatchResponse) BatchResponse {
	merged := BatchResponse{}
	totalResults := 0
	for _, part := range parts {
		totalResults += len(part.Results)
		merged.Succeeded += part.Succeeded
		merged.Failed += part.Failed
	}
	merged.Results = make([]BatchResult, 0, totalResults)
	for _, part := range parts {
		merged.Results = append(merged.Results, part.Results...)
	}
	return merged
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
