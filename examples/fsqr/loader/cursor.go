// Cursor — отслеживание прогресса загрузки для resume.
// Хранится как JSON файл на PVC, обновляется каждые N строк.
package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Cursor хранит позицию в потоке загрузки.
type Cursor struct {
	Stage          string    `json:"stage"`
	FileIndex      int       `json:"file_index"`
	RowOffset      int       `json:"row_offset"`
	TotalProcessed int       `json:"total_processed"`
	TotalFailed    int       `json:"total_failed"`
	CategoriesDone bool      `json:"categories_done"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// cursorTracker — потокобезопасный трекер прогресса с периодическим сохранением.
type cursorTracker struct {
	mu        sync.Mutex
	cursor    Cursor
	path      string
	saveEvery int
	dirty     bool
}

// newCursorTracker создаёт трекер. Если файл существует — загружает предыдущее состояние.
func newCursorTracker(dataDir string, saveEvery int) (*cursorTracker, error) {
	path := filepath.Join(filepath.Clean(dataDir), "cursor.json")
	ct := &cursorTracker{
		path:      path,
		saveEvery: saveEvery,
	}

	data, err := os.ReadFile(path)
	if err == nil {
		if err := json.Unmarshal(data, &ct.cursor); err != nil {
			return nil, fmt.Errorf("parse cursor %s: %w", path, err)
		}
		log.Printf("resume from cursor: stage=%s file=%d offset=%d processed=%d",
			ct.cursor.Stage, ct.cursor.FileIndex, ct.cursor.RowOffset, ct.cursor.TotalProcessed)
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("read cursor %s: %w", path, err)
	}

	return ct, nil
}

// Get возвращает копию текущего cursor.
func (ct *cursorTracker) Get() Cursor {
	ct.mu.Lock()
	defer ct.mu.Unlock()
	return ct.cursor
}

// SetStage устанавливает текущий этап и сохраняет.
func (ct *cursorTracker) SetStage(stage string) {
	ct.mu.Lock()
	ct.cursor.Stage = stage
	ct.cursor.UpdatedAt = time.Now()
	ct.dirty = true
	ct.mu.Unlock()
	ct.forceSave()
}

// SetCategoriesDone помечает этап категорий завершённым.
func (ct *cursorTracker) SetCategoriesDone() {
	ct.mu.Lock()
	ct.cursor.CategoriesDone = true
	ct.cursor.UpdatedAt = time.Now()
	ct.dirty = true
	ct.mu.Unlock()
	ct.forceSave()
}

// Advance продвигает cursor на processed строк.
// Автоматически сохраняет каждые saveEvery строк.
func (ct *cursorTracker) Advance(fileIndex, rowOffset, processed, failed int) {
	ct.mu.Lock()
	ct.cursor.FileIndex = fileIndex
	ct.cursor.RowOffset = rowOffset
	ct.cursor.TotalProcessed += processed
	ct.cursor.TotalFailed += failed
	ct.cursor.UpdatedAt = time.Now()
	ct.dirty = true
	shouldSave := ct.cursor.TotalProcessed%ct.saveEvery < processed
	ct.mu.Unlock()

	if shouldSave {
		ct.forceSave()
	}
}

// forceSave записывает cursor на диск.
func (ct *cursorTracker) forceSave() {
	ct.mu.Lock()
	if !ct.dirty {
		ct.mu.Unlock()
		return
	}
	data, err := json.MarshalIndent(ct.cursor, "", "  ")
	if err != nil {
		ct.mu.Unlock()
		log.Printf("cursor marshal error: %v", err)
		return
	}
	ct.dirty = false
	ct.mu.Unlock()

	tmp := ct.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		log.Printf("cursor write error: %v", err)
		ct.mu.Lock()
		ct.dirty = true
		ct.mu.Unlock()
		return
	}
	if err := os.Rename(tmp, ct.path); err != nil {
		log.Printf("cursor rename error: %v", err)
		ct.mu.Lock()
		ct.dirty = true
		ct.mu.Unlock()
	}
}

// Done помечает загрузку завершённой.
func (ct *cursorTracker) Done() {
	ct.SetStage("done")
}

// Reset сбрасывает cursor для начала с нуля.
func (ct *cursorTracker) Reset() {
	ct.mu.Lock()
	ct.cursor = Cursor{}
	ct.dirty = true
	ct.mu.Unlock()
	ct.forceSave()
}
