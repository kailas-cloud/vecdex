package embedding

import (
	"context"
	"fmt"
	"sync"

	"github.com/kailas-cloud/vecdex/internal/domain"
)

const (
	// defaultParallelBatchSize is the per-worker sub-batch size for large requests.
	defaultParallelBatchSize = 10
	// defaultParallelBatchWorker is the default number of concurrent sub-batches.
	defaultParallelBatchWorker = 10
)

// ParallelBatchEmbedder shards large batch requests into smaller concurrent
// sub-batches while preserving output order.
type ParallelBatchEmbedder struct {
	inner       domain.Embedder
	batchSize   int
	parallelism int
}

// NewParallelBatchEmbedder wraps an embedder with concurrent batch execution.
func NewParallelBatchEmbedder(
	inner domain.Embedder,
	batchSize int,
	parallelism int,
) *ParallelBatchEmbedder {
	if batchSize <= 0 {
		batchSize = defaultParallelBatchSize
	}
	if parallelism <= 0 {
		parallelism = defaultParallelBatchWorker
	}
	return &ParallelBatchEmbedder{
		inner:       inner,
		batchSize:   batchSize,
		parallelism: parallelism,
	}
}

// Embed delegates to the wrapped embedder unchanged.
func (e *ParallelBatchEmbedder) Embed(
	ctx context.Context, text string,
) (domain.EmbeddingResult, error) {
	res, err := e.inner.Embed(ctx, text)
	if err != nil {
		return domain.EmbeddingResult{}, fmt.Errorf("parallel embed: %w", err)
	}
	return res, nil
}

// BatchEmbed shards large requests and executes them concurrently.
func (e *ParallelBatchEmbedder) BatchEmbed(
	ctx context.Context, texts []string,
) (domain.BatchEmbeddingResult, error) {
	if len(texts) == 0 {
		return domain.BatchEmbeddingResult{}, nil
	}
	if len(texts) <= e.batchSize || e.parallelism <= 1 {
		return e.embedInner(ctx, texts)
	}

	jobs := shardTexts(texts, e.batchSize)
	if len(jobs) == 1 {
		return e.embedInner(ctx, texts)
	}

	return e.runParallelBatch(ctx, jobs)
}

type batchJob struct {
	index int
	texts []string
}

type batchResult struct {
	index  int
	result domain.BatchEmbeddingResult
	err    error
}

func (e *ParallelBatchEmbedder) runParallelBatch(
	ctx context.Context, jobs [][]string,
) (domain.BatchEmbeddingResult, error) {
	workers := minInt(e.parallelism, len(jobs))
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)

	jobCh := make(chan batchJob)
	resultCh := make(chan batchResult, len(jobs))

	var wg sync.WaitGroup
	e.startWorkers(ctx, &wg, workers, jobCh, resultCh, cancel)
	go enqueueBatchJobs(ctx, jobCh, jobs)

	results, err := collectBatchResults(ctx, &wg, resultCh, len(jobs))
	if err != nil {
		return domain.BatchEmbeddingResult{}, err
	}
	return mergeBatchResults(results), nil
}

func (e *ParallelBatchEmbedder) startWorkers(
	ctx context.Context,
	wg *sync.WaitGroup,
	workers int,
	jobCh <-chan batchJob,
	resultCh chan<- batchResult,
	cancel context.CancelCauseFunc,
) {
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for job := range jobCh {
				res, err := e.embedInner(ctx, job.texts)
				select {
				case resultCh <- batchResult{index: job.index, result: res, err: err}:
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

func enqueueBatchJobs(
	ctx context.Context,
	jobCh chan<- batchJob,
	jobs [][]string,
) {
	defer close(jobCh)
	for i, chunk := range jobs {
		select {
		case jobCh <- batchJob{index: i, texts: chunk}:
		case <-ctx.Done():
			return
		}
	}
}

func collectBatchResults(
	ctx context.Context,
	wg *sync.WaitGroup,
	resultCh <-chan batchResult,
	expected int,
) ([]domain.BatchEmbeddingResult, error) {
	results := make([]domain.BatchEmbeddingResult, expected)
	for range expected {
		select {
		case <-ctx.Done():
			wg.Wait()
			return nil, fmt.Errorf("parallel batch embed: %w", context.Cause(ctx))
		case res := <-resultCh:
			if res.err != nil {
				wg.Wait()
				return nil, res.err
			}
			results[res.index] = res.result
		}
	}
	wg.Wait()
	return results, nil
}

func (e *ParallelBatchEmbedder) embedInner(
	ctx context.Context, texts []string,
) (domain.BatchEmbeddingResult, error) {
	if be, ok := e.inner.(domain.BatchEmbedder); ok {
		res, err := be.BatchEmbed(ctx, texts)
		if err != nil {
			return domain.BatchEmbeddingResult{}, fmt.Errorf("parallel inner batch embed: %w", err)
		}
		return res, nil
	}
	res, err := domain.BatchFallback(ctx, e.inner, texts)
	if err != nil {
		return domain.BatchEmbeddingResult{}, fmt.Errorf("parallel batch embed fallback: %w", err)
	}
	return res, nil
}

func shardTexts(texts []string, size int) [][]string {
	if size <= 0 {
		size = len(texts)
	}
	chunks := make([][]string, 0, (len(texts)+size-1)/size)
	for start := 0; start < len(texts); start += size {
		end := start + size
		if end > len(texts) {
			end = len(texts)
		}
		chunks = append(chunks, texts[start:end])
	}
	return chunks
}

func mergeBatchResults(parts []domain.BatchEmbeddingResult) domain.BatchEmbeddingResult {
	totalEmbeddings := 0
	totalPrompt := 0
	totalTokens := 0
	for _, part := range parts {
		totalEmbeddings += len(part.Embeddings)
		totalPrompt += part.PromptTokens
		totalTokens += part.TotalTokens
	}

	merged := domain.BatchEmbeddingResult{
		Embeddings:   make([][]float32, 0, totalEmbeddings),
		PromptTokens: totalPrompt,
		TotalTokens:  totalTokens,
	}
	for _, part := range parts {
		merged.Embeddings = append(merged.Embeddings, part.Embeddings...)
	}
	return merged
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
