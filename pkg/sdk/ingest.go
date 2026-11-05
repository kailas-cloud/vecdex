package vecdex

import (
	"context"
	"fmt"
	"sync"
)

// Default ingestion limits apply when IngestorConfig omits a field.
const (
	DefaultIngestBatchSize   = 100
	DefaultIngestParallelism = 10
)

// IngestorConfig controls source-document batch ingestion.
type IngestorConfig struct {
	BatchSize   int
	Parallelism int
}

// TextIngestor uploads complete source documents in parallel batches.
// Fragmentation is private to the shared ingestion pipeline.
type TextIngestor struct {
	docs        *DocumentService
	batchSize   int
	parallelism int
}

// NewTextIngestor creates an ingestor over the SDK document service.
func NewTextIngestor(
	docs *DocumentService,
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
		batchSize:   batchSize,
		parallelism: parallelism,
	}
}

// Upsert stores each source ID once, regardless of its internal fragment count.
func (i *TextIngestor) Upsert(
	ctx context.Context,
	docs []BatchUpsertItem,
) (BatchResponse, error) {
	if len(docs) == 0 {
		return BatchResponse{}, nil
	}
	batches := shardDocuments(docs, i.batchSize)
	return i.upsertParallel(ctx, batches)
}

type ingestJob struct {
	index int
	docs  []BatchUpsertItem
}

type ingestResult struct {
	index int
	resp  BatchResponse
	err   error
}

func (i *TextIngestor) upsertParallel(
	ctx context.Context,
	batches [][]BatchUpsertItem,
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
	batches [][]BatchUpsertItem,
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

func shardDocuments(docs []BatchUpsertItem, size int) [][]BatchUpsertItem {
	if size <= 0 {
		size = len(docs)
	}
	batches := make([][]BatchUpsertItem, 0, (len(docs)+size-1)/size)
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
