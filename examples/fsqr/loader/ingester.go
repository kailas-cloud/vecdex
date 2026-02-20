// Worker pool для параллельной загрузки venues в vecdex.
// Reader → channel([]Venue) → N workers → BatchUpsert → Valkey.
package main

import (
	"context"
	"log"
	"sync"
	"sync/atomic"
	"time"

	vecdex "github.com/kailas-cloud/vecdex/pkg/sdk"
)

// ingester — worker pool для batch upsert.
type ingester struct {
	idx       *vecdex.TypedIndex[Venue]
	workers   int
	batchSize int
	metrics   *loaderMetrics
	cursor    *cursorTracker
}

// batchItem — один батч для отправки worker'у.
type batchItem struct {
	venues    []Venue
	fileIndex int
	rowOffset int
}

// ingestResult — итоги загрузки.
type ingestResult struct {
	Processed int64
	Failed    int64
	Duration  time.Duration
}

// Run запускает pipeline: reader → workers → Valkey.
func (ing *ingester) Run(
	ctx context.Context,
	reader *parquetReader,
	cats *categoryMap,
	maxRows int,
) (ingestResult, error) {
	cur := ing.cursor.Get()

	batches := make(chan batchItem, ing.workers*2)
	var wg sync.WaitGroup
	var totalProcessed, totalFailed atomic.Int64

	start := time.Now()

	for i := 0; i < ing.workers; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			ing.worker(ctx, workerID, batches, &totalProcessed, &totalFailed)
		}(i)
	}

	var readerErr error
	go func() {
		defer close(batches)
		readerErr = ing.produce(
			ctx, reader, cats,
			cur.FileIndex, cur.RowOffset, maxRows, batches,
		)
	}()

	wg.Wait()

	result := ingestResult{
		Processed: totalProcessed.Load(),
		Failed:    totalFailed.Load(),
		Duration:  time.Since(start),
	}
	if readerErr != nil {
		return result, readerErr
	}
	return result, nil
}

// produce читает parquet и формирует батчи.
func (ing *ingester) produce(
	ctx context.Context,
	reader *parquetReader,
	cats *categoryMap,
	fileIndex, rowOffset, maxRows int,
	out chan<- batchItem,
) error {
	var batch []Venue
	currentFile := fileIndex
	currentRow := rowOffset

	err := reader.ReadPlaces(fileIndex, rowOffset, maxRows,
		func(row *fsqPlaceRow, seq int) bool {
			select {
			case <-ctx.Done():
				return false
			default:
			}

			for i, catID := range row.FSQCategoryIDs {
				label := ""
				if i < len(row.FSQCategoryLabel) {
					label = row.FSQCategoryLabel[i]
				}
				cats.Add(catID, label)
			}

			venue, ok := toVenue(row, seq, cats)
			if !ok {
				if ing.metrics != nil {
					ing.metrics.rowsFailed.WithLabelValues(
						"venues", "no_coords",
					).Inc()
				}
				return true
			}

			batch = append(batch, venue)
			currentRow = seq + 1

			if len(batch) >= ing.batchSize {
				out <- batchItem{
					venues:    batch,
					fileIndex: currentFile,
					rowOffset: currentRow,
				}
				batch = make([]Venue, 0, ing.batchSize)
			}
			return true
		})

	if len(batch) > 0 {
		out <- batchItem{
			venues:    batch,
			fileIndex: currentFile,
			rowOffset: currentRow,
		}
	}

	return err
}

// worker обрабатывает батчи из channel.
func (ing *ingester) worker(
	ctx context.Context,
	id int,
	batches <-chan batchItem,
	processed, failed *atomic.Int64,
) {
	for batch := range batches {
		ing.processBatch(ctx, id, batch, processed, failed)
	}
}

func (ing *ingester) processBatch(
	ctx context.Context,
	id int,
	batch batchItem,
	processed, failed *atomic.Int64,
) {
	start := time.Now()

	resp, err := ing.idx.UpsertBatch(ctx, batch.venues)

	dur := time.Since(start).Seconds()
	if ing.metrics != nil {
		ing.metrics.batchDuration.WithLabelValues("venues").Observe(dur)
		ing.metrics.batchesTotal.WithLabelValues("venues").Inc()
	}

	if err != nil {
		log.Printf("worker %d: batch upsert error: %v", id, err)
		failed.Add(int64(len(batch.venues)))
		if ing.metrics != nil {
			ing.metrics.rowsFailed.WithLabelValues(
				"venues", "batch_error",
			).Add(float64(len(batch.venues)))
		}
		return
	}

	processed.Add(int64(resp.Succeeded))
	failed.Add(int64(resp.Failed))

	if ing.metrics != nil {
		ing.metrics.rowsProcessed.WithLabelValues("venues").Add(
			float64(resp.Succeeded),
		)
		if resp.Failed > 0 {
			ing.metrics.rowsFailed.WithLabelValues(
				"venues", "item_error",
			).Add(float64(resp.Failed))
		}
	}

	ing.cursor.Advance(
		batch.fileIndex, batch.rowOffset,
		resp.Succeeded, resp.Failed,
	)

	total := processed.Load()
	if total%10000 < int64(ing.batchSize) {
		log.Printf("venues: %d processed, %d failed", total, failed.Load())
	}
}

// loadCategories загружает категории в vecdex с embeddings.
func loadCategories(
	ctx context.Context,
	idx *vecdex.TypedIndex[Category],
	cats *categoryMap,
	m *loaderMetrics,
) {
	all := cats.Categories()
	log.Printf("loading %d categories into vecdex...", len(all))

	for i, cat := range all {
		if _, err := idx.Upsert(ctx, cat); err != nil {
			log.Printf("category upsert failed: %s: %v", cat.ID, err)
			if m != nil {
				m.rowsFailed.WithLabelValues(
					"categories", "upsert_error",
				).Inc()
			}
			continue
		}
		if m != nil {
			m.rowsProcessed.WithLabelValues("categories").Inc()
		}
		if (i+1)%100 == 0 {
			log.Printf("categories: %d/%d", i+1, len(all))
		}
	}

	log.Printf("categories: %d/%d done", len(all), len(all))
}
