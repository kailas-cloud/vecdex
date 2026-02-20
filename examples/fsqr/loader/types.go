// Типы данных для FSQ OS Places ingest pipeline.
// Venue — гео-коллекция (без embeddings), Category — текстовая (с embeddings).
// FSQ UUID категорий маппятся в sequential int для компактности хранения.
package main

import (
	"fmt"

	vecdex "github.com/kailas-cloud/vecdex/pkg/sdk"
)

// Category — текстовая коллекция, semantic search по label.
type Category struct {
	ID    string `vecdex:"id,id"`        // Sequential int as string: "1", "2", ...
	Label string `vecdex:"label,content"` // "Food > Italian Restaurant" → embedding
	FSQ   string `vecdex:"fsq,tag"`      // Original FSQ UUID для reverse lookup
}

// Venue — гео-коллекция, без embeddings.
type Venue struct {
	ID   string  `vecdex:"id,id"`       // Sequential int as string
	Name string  `vecdex:"name,tag"`    // Place name (exact match)
	Cat  int     `vecdex:"cat,numeric"` // Category int ID
	Lat  float64 `vecdex:"lat,geo_lat"`
	Lon  float64 `vecdex:"lon,geo_lon"`
}

// fsqPlaceRow — raw row из FSQ OS Places parquet.
type fsqPlaceRow struct {
	FSQPlaceID       string   `parquet:"fsq_place_id"`
	Name             string   `parquet:"name"`
	Latitude         *float64 `parquet:"latitude"`
	Longitude        *float64 `parquet:"longitude"`
	Address          *string  `parquet:"address"`
	Locality         *string  `parquet:"locality"`
	Region           *string  `parquet:"region"`
	Country          *string  `parquet:"country"`
	FSQCategoryIDs   []string `parquet:"fsq_category_ids,list"`
	FSQCategoryLabel []string `parquet:"fsq_category_labels,list"`
	DateClosed       *string  `parquet:"date_closed"`
}

// fsqCategoryRow — row из categories parquet.
type fsqCategoryRow struct {
	ID    string  `parquet:"id"`
	Label *string `parquet:"label"`
}

// categoryMap строит и хранит маппинг FSQ UUID → sequential int.
type categoryMap struct {
	byFSQ map[string]int // FSQ UUID → int ID
	byID  map[int]string // int ID → FSQ UUID
	labels map[string]string // FSQ UUID → human label
	nextID int
}

func newCategoryMap() *categoryMap {
	return &categoryMap{
		byFSQ:  make(map[string]int),
		byID:   make(map[int]string),
		labels: make(map[string]string),
		nextID: 1,
	}
}

// Add регистрирует категорию, возвращает int ID.
// Если уже зарегистрирована — возвращает существующий ID.
func (m *categoryMap) Add(fsqID, label string) int {
	if id, ok := m.byFSQ[fsqID]; ok {
		// Обновляем label если получили более полный.
		if label != "" && m.labels[fsqID] == "" {
			m.labels[fsqID] = label
		}
		return id
	}
	id := m.nextID
	m.nextID++
	m.byFSQ[fsqID] = id
	m.byID[id] = fsqID
	if label != "" {
		m.labels[fsqID] = label
	}
	return id
}

// Lookup возвращает int ID для FSQ UUID. Если не найден — 0.
func (m *categoryMap) Lookup(fsqID string) int {
	return m.byFSQ[fsqID]
}

// Categories возвращает все категории для загрузки в vecdex.
func (m *categoryMap) Categories() []Category {
	cats := make([]Category, 0, len(m.byFSQ))
	for fsqID, id := range m.byFSQ {
		label := m.labels[fsqID]
		if label == "" {
			label = fsqID // fallback: используем UUID если label не получили
		}
		cats = append(cats, Category{
			ID:    itoa(id),
			Label: label,
			FSQ:   fsqID,
		})
	}
	return cats
}

// Len возвращает количество зарегистрированных категорий.
func (m *categoryMap) Len() int {
	return len(m.byFSQ)
}

func itoa(n int) string {
	return fmt.Sprintf("%d", n)
}

// toVenue конвертирует raw parquet row в Venue с int category.
func toVenue(row fsqPlaceRow, seq int, cats *categoryMap) (Venue, bool) {
	if row.Latitude == nil || row.Longitude == nil {
		return Venue{}, false
	}
	catID := 0
	if len(row.FSQCategoryIDs) > 0 {
		catID = cats.Lookup(row.FSQCategoryIDs[0])
	}
	return Venue{
		ID:   itoa(seq),
		Name: row.Name,
		Cat:  catID,
		Lat:  *row.Latitude,
		Lon:  *row.Longitude,
	}, true
}

// vecdex SDK embedder interface compatibility.
var _ vecdex.Embedder = (*NebiusEmbedder)(nil)
