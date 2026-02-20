// Потоковое чтение FSQ OS Places parquet файлов.
// Поддерживает skip для resume — пропускает row groups до нужного offset.
package main

import (
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sort"

	"github.com/parquet-go/parquet-go"
)

// parquetReader читает places parquet файлы с поддержкой skip.
type parquetReader struct {
	dataDir string
	files   []string // отсортированные пути к parquet файлам
}

// newParquetReader создаёт reader, сканирует dataDir на parquet файлы.
func newParquetReader(dataDir string) (*parquetReader, error) {
	pattern := filepath.Join(dataDir, "*.parquet")
	files, err := filepath.Glob(pattern)
	if err != nil {
		return nil, fmt.Errorf("glob parquet files: %w", err)
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("no parquet files found in %s", dataDir)
	}
	sort.Strings(files)
	log.Printf("found %d parquet files in %s", len(files), dataDir)
	return &parquetReader{dataDir: dataDir, files: files}, nil
}

// FileCount возвращает количество parquet файлов.
func (r *parquetReader) FileCount() int {
	return len(r.files)
}

// readPlacesCallback — callback для каждого row.
// seq — глобальный sequential номер строки (для ID).
// Возвращает false чтобы остановить чтение.
type readPlacesCallback func(row fsqPlaceRow, seq int) bool

// ReadPlaces читает venues начиная с fileIndex/rowOffset.
// maxRows=0 — без лимита. Вызывает callback для каждой строки.
func (r *parquetReader) ReadPlaces(fileIndex, rowOffset int, maxRows int, cb readPlacesCallback) error {
	seq := rowOffset // sequential counter начинается с offset (для resume)
	remaining := maxRows

	for fi := fileIndex; fi < len(r.files); fi++ {
		skipRows := 0
		if fi == fileIndex {
			skipRows = rowOffset
		}

		n, err := r.readFile(r.files[fi], skipRows, remaining, seq, cb)
		if err != nil {
			return fmt.Errorf("read %s: %w", filepath.Base(r.files[fi]), err)
		}
		seq += n

		if maxRows > 0 {
			remaining -= n
			if remaining <= 0 {
				break
			}
		}
	}
	return nil
}

// readFile читает один parquet файл с пропуском первых skipRows строк.
func (r *parquetReader) readFile(path string, skipRows, maxRows, startSeq int, cb readPlacesCallback) (int, error) {
	cleanPath := filepath.Clean(path)
	f, err := os.Open(cleanPath)
	if err != nil {
		return 0, fmt.Errorf("open: %w", err)
	}
	defer func() { _ = f.Close() }()

	stat, err := f.Stat()
	if err != nil {
		return 0, fmt.Errorf("stat: %w", err)
	}

	pf, err := parquet.OpenFile(f, stat.Size())
	if err != nil {
		return 0, fmt.Errorf("open parquet: %w", err)
	}

	read := 0
	skipped := 0
	seq := startSeq

	for _, rg := range pf.RowGroups() {
		rgRows := int(rg.NumRows())

		// Skip целые row groups если ещё не дошли до offset.
		if skipped+rgRows <= skipRows {
			skipped += rgRows
			continue
		}

		rows := parquet.NewRowGroupReader(rg)
		buf := make([]parquet.Row, 1000)

		for {
			n, readErr := rows.ReadRows(buf)
			for i := 0; i < n; i++ {
				if skipped < skipRows {
					skipped++
					continue
				}

				var place fsqPlaceRow
				if err := pf.Schema().Reconstruct(&place, buf[i]); err != nil {
					log.Printf("skip row: reconstruct error: %v", err)
					continue
				}

				if !cb(place, seq) {
					return read, nil
				}
				seq++
				read++

				if maxRows > 0 && read >= maxRows {
					return read, nil
				}
			}

			if readErr != nil {
				if readErr == io.EOF {
					break
				}
				return read, fmt.Errorf("read rows: %w", readErr)
			}
		}
	}

	return read, nil
}

// ReadCategories читает categories parquet и заполняет categoryMap.
func (r *parquetReader) ReadCategories(catFile string, cats *categoryMap) error {
	cleanPath := filepath.Clean(catFile)
	f, err := os.Open(cleanPath)
	if err != nil {
		return fmt.Errorf("open categories: %w", err)
	}
	defer func() { _ = f.Close() }()

	stat, err := f.Stat()
	if err != nil {
		return fmt.Errorf("stat categories: %w", err)
	}

	pf, err := parquet.OpenFile(f, stat.Size())
	if err != nil {
		return fmt.Errorf("open parquet: %w", err)
	}

	for _, rg := range pf.RowGroups() {
		rows := parquet.NewRowGroupReader(rg)
		buf := make([]parquet.Row, 1000)

		for {
			n, readErr := rows.ReadRows(buf)
			for i := 0; i < n; i++ {
				var cat fsqCategoryRow
				if err := pf.Schema().Reconstruct(&cat, buf[i]); err != nil {
					log.Printf("skip category row: %v", err)
					continue
				}
				label := ""
				if cat.Label != nil {
					label = *cat.Label
				}
				cats.Add(cat.ID, label)
			}

			if readErr != nil {
				if readErr == io.EOF {
					break
				}
				return fmt.Errorf("read category rows: %w", readErr)
			}
		}
	}

	log.Printf("loaded %d categories from %s", cats.Len(), filepath.Base(catFile))
	return nil
}
