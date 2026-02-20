// Потоковое чтение FSQ OS Places parquet файлов.
// Поддерживает skip для resume — пропускает row groups до нужного offset.
package main

import (
	"errors"
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
	files   []string
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

// readPlacesCallback вызывается для каждого row.
// seq — глобальный sequential номер строки. Возвращает false для остановки.
type readPlacesCallback func(row *fsqPlaceRow, seq int) bool

// ReadPlaces читает venues начиная с fileIndex/rowOffset.
// maxRows=0 — без лимита. Вызывает callback для каждой строки.
func (r *parquetReader) ReadPlaces(fileIndex, rowOffset, maxRows int, cb readPlacesCallback) error {
	seq := rowOffset
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

// placeColumns — индексы нужных колонок в parquet файле.
type placeColumns struct {
	fsqPlaceID     int
	name           int
	latitude       int
	longitude      int
	address        int
	locality       int
	region         int
	country        int
	fsqCategoryIDs int // list column — leaf index
	fsqCatLabels   int // list column — leaf index
	dateClosed     int
}

// resolvePlaceColumns находит leaf-level индексы колонок по имени.
func resolvePlaceColumns(pf *parquet.File) placeColumns {
	cols := placeColumns{
		fsqPlaceID: -1, name: -1, latitude: -1, longitude: -1,
		address: -1, locality: -1, region: -1, country: -1,
		fsqCategoryIDs: -1, fsqCatLabels: -1, dateClosed: -1,
	}
	for i, path := range pf.Schema().Columns() {
		if len(path) == 0 {
			continue
		}
		switch path[0] {
		case "fsq_place_id":
			cols.fsqPlaceID = i
		case "name":
			cols.name = i
		case "latitude":
			cols.latitude = i
		case "longitude":
			cols.longitude = i
		case "address":
			cols.address = i
		case "locality":
			cols.locality = i
		case "region":
			cols.region = i
		case "country":
			cols.country = i
		case "fsq_category_ids":
			cols.fsqCategoryIDs = i
		case "fsq_category_labels":
			cols.fsqCatLabels = i
		case "date_closed":
			cols.dateClosed = i
		}
	}
	return cols
}

// readFile читает один parquet файл с пропуском первых skipRows строк.
func (r *parquetReader) readFile(
	path string, skipRows, maxRows, startSeq int, cb readPlacesCallback,
) (int, error) {
	h, err := openParquet(path)
	if err != nil {
		return 0, err
	}
	defer h.Close()

	cols := resolvePlaceColumns(h.pf)

	read := 0
	skipped := 0
	seq := startSeq

	for _, rg := range h.pf.RowGroups() {
		rgRows := int(rg.NumRows())
		if skipped+rgRows <= skipRows {
			skipped += rgRows
			continue
		}

		n, done, err := r.readRowGroup(rg, cols, skipRows, maxRows, &skipped, &read, &seq, cb)
		if err != nil {
			return read, err
		}
		read += n
		if done {
			break
		}
	}

	return read, nil
}

func (r *parquetReader) readRowGroup(
	rg parquet.RowGroup,
	cols placeColumns,
	skipRows, maxRows int,
	skipped, read, seq *int,
	cb readPlacesCallback,
) (processed int, done bool, err error) {
	rows := parquet.NewRowGroupReader(rg)
	buf := make([]parquet.Row, 1000)
	n := 0

	for {
		cnt, readErr := rows.ReadRows(buf)
		for i := 0; i < cnt; i++ {
			if *skipped < skipRows {
				*skipped++
				continue
			}

			place := rowToPlace(buf[i], cols)

			if !cb(&place, *seq) {
				return n, true, nil
			}
			*seq++
			n++

			if maxRows > 0 && *read+n >= maxRows {
				return n, true, nil
			}
		}

		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				break
			}
			return n, false, fmt.Errorf("read rows: %w", readErr)
		}
	}

	return n, false, nil
}

// rowToPlace извлекает fsqPlaceRow из generic parquet row по индексам колонок.
func rowToPlace(row parquet.Row, cols placeColumns) fsqPlaceRow {
	var p fsqPlaceRow
	var catIDs, catLabels []string

	for _, v := range row {
		col := v.Column()
		switch col {
		case cols.fsqPlaceID:
			p.FSQPlaceID = v.String()
		case cols.name:
			p.Name = v.String()
		case cols.latitude:
			if !v.IsNull() {
				f := v.Double()
				p.Latitude = &f
			}
		case cols.longitude:
			if !v.IsNull() {
				f := v.Double()
				p.Longitude = &f
			}
		case cols.address:
			if !v.IsNull() {
				s := v.String()
				p.Address = &s
			}
		case cols.locality:
			if !v.IsNull() {
				s := v.String()
				p.Locality = &s
			}
		case cols.region:
			if !v.IsNull() {
				s := v.String()
				p.Region = &s
			}
		case cols.country:
			if !v.IsNull() {
				s := v.String()
				p.Country = &s
			}
		case cols.fsqCategoryIDs:
			if !v.IsNull() {
				catIDs = append(catIDs, v.String())
			}
		case cols.fsqCatLabels:
			if !v.IsNull() {
				catLabels = append(catLabels, v.String())
			}
		case cols.dateClosed:
			if !v.IsNull() {
				s := v.String()
				p.DateClosed = &s
			}
		}
	}

	p.FSQCategoryIDs = catIDs
	p.FSQCategoryLabel = catLabels
	return p
}

// parquetHandle wraps parquet.File + underlying os.File for proper cleanup.
type parquetHandle struct {
	pf   *parquet.File
	file *os.File
}

func (h *parquetHandle) Close() {
	_ = h.file.Close()
}

func openParquet(path string) (*parquetHandle, error) {
	cleanPath := filepath.Clean(path)
	f, err := os.Open(cleanPath)
	if err != nil {
		return nil, fmt.Errorf("open: %w", err)
	}

	stat, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("stat: %w", err)
	}

	pf, err := parquet.OpenFile(f, stat.Size())
	if err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("open parquet: %w", err)
	}
	return &parquetHandle{pf: pf, file: f}, nil
}

// ReadCategories читает categories parquet и заполняет categoryMap.
// Использует generic reader вместо Schema.Reconstruct — parquet-go
// падает на Reconstruct если схема содержит nullable колонки с complex types.
func (r *parquetReader) ReadCategories(catFile string, cats *categoryMap) error {
	h, err := openParquet(catFile)
	if err != nil {
		return fmt.Errorf("open categories: %w", err)
	}
	defer h.Close()

	// Находим индексы нужных колонок по имени.
	schema := h.pf.Schema()
	idIdx := -1
	labelIdx := -1
	for i, col := range schema.Columns() {
		switch col[0] {
		case "category_id":
			idIdx = i
		case "category_label":
			labelIdx = i
		}
	}
	if idIdx < 0 {
		return fmt.Errorf("category_id column not found in parquet schema")
	}

	for _, rg := range h.pf.RowGroups() {
		rows := parquet.NewRowGroupReader(rg)
		buf := make([]parquet.Row, 1000)

		for {
			n, readErr := rows.ReadRows(buf)
			for i := 0; i < n; i++ {
				row := buf[i]
				id := ""
				label := ""
				for _, v := range row {
					if v.Column() == idIdx {
						id = v.String()
					}
					if v.Column() == labelIdx && !v.IsNull() {
						label = v.String()
					}
				}
				if id != "" {
					cats.Add(id, label)
				}
			}

			if readErr != nil {
				if errors.Is(readErr, io.EOF) {
					break
				}
				return fmt.Errorf("read category rows: %w", readErr)
			}
		}
	}

	log.Printf("loaded %d categories from %s", cats.Len(), filepath.Base(catFile))
	return nil
}
