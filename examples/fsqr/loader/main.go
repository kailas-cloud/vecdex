// Загрузчик данных FSQ Open Places в vecdex.
// Читает JSONL, создаёт коллекции categories и venues, загружает батчами.
//
// Использование:
//
//	go run ./examples/fsqr/loader/ -data path/to/places.jsonl
//
// Env vars:
//
//	VALKEY_ADDR     — адрес Valkey (default: localhost:6379)
//	VALKEY_PASSWORD — пароль Valkey
//	NEBIUS_API_KEY  — API ключ Nebius Inference (для эмбеддингов категорий)
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"

	vecdex "github.com/kailas-cloud/vecdex/pkg/sdk"
)

const batchSize = 50

// fsqPlace — структура строки из FSQ Open Places JSONL.
type fsqPlace struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Lat         float64  `json:"lat"`
	Lon         float64  `json:"lon"`
	Address     string   `json:"address"`
	Locality    string   `json:"locality"`
	Country     string   `json:"country"`
	CategoryIDs []string `json:"category_ids"`
}

// Venue — гео-коллекция venues.
type Venue struct {
	ID    string  `vecdex:"id,id"`
	Name  string  `vecdex:"name,tag"`
	CatID string  `vecdex:"category_id,tag"`
	Lat   float64 `vecdex:"latitude,geo_lat"`
	Lon   float64 `vecdex:"longitude,geo_lon"`
}

// Category — текстовая коллекция категорий (для семантического поиска).
type Category struct {
	ID   string `vecdex:"id,id"`
	Name string `vecdex:"name,content"`
}

func main() {
	dataFile := flag.String("data", "tests/testdata/paphos_places.jsonl", "path to FSQ places JSONL")
	flag.Parse()

	if err := run(*dataFile); err != nil {
		log.Fatal(err)
	}
}

func run(dataFile string) error {
	ctx := context.Background()

	addr := env("VALKEY_ADDR", "localhost:6379")
	password := env("VALKEY_PASSWORD", "")
	apiKey := os.Getenv("NEBIUS_API_KEY")

	embedder := NewNebiusEmbedder(apiKey)

	client, err := vecdex.New(
		ctx,
		vecdex.WithRedis(addr, password),
		vecdex.WithEmbedder(embedder),
		vecdex.WithVectorDimensions(4096),
	)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	defer client.Close()

	places, err := readJSONL(dataFile)
	if err != nil {
		return err
	}
	log.Printf("loaded %d places from %s", len(places), dataFile)

	// Извлекаем уникальные категории из данных.
	categories := extractCategories(places)
	log.Printf("found %d unique categories", len(categories))

	// Создаём и наполняем коллекцию категорий.
	catIdx, err := vecdex.NewIndex[Category](client, "categories")
	if err != nil {
		return fmt.Errorf("init categories: %w", err)
	}
	if err := catIdx.Ensure(ctx); err != nil {
		return fmt.Errorf("ensure categories: %w", err)
	}
	if err := loadCategories(ctx, catIdx, categories); err != nil {
		return err
	}

	// Создаём и наполняем коллекцию venues.
	venueIdx, err := vecdex.NewIndex[Venue](client, "venues")
	if err != nil {
		return fmt.Errorf("init venues: %w", err)
	}
	if err := venueIdx.Ensure(ctx); err != nil {
		return fmt.Errorf("ensure venues: %w", err)
	}
	if err := loadVenues(ctx, venueIdx, places); err != nil {
		return err
	}

	catCount, _ := catIdx.Count(ctx)
	venueCount, _ := venueIdx.Count(ctx)
	log.Printf("done: %d categories, %d venues", catCount, venueCount)

	return nil
}

func readJSONL(path string) ([]fsqPlace, error) {
	cleanPath := filepath.Clean(path)
	f, err := os.Open(cleanPath)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", cleanPath, err)
	}
	defer func() { _ = f.Close() }()

	var places []fsqPlace
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var p fsqPlace
		if err := json.Unmarshal(scanner.Bytes(), &p); err != nil {
			return nil, fmt.Errorf("parse JSONL: %w", err)
		}
		places = append(places, p)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read JSONL: %w", err)
	}
	return places, nil
}

func extractCategories(places []fsqPlace) []Category {
	seen := make(map[string]bool)
	var cats []Category
	for _, p := range places {
		for _, catID := range p.CategoryIDs {
			if seen[catID] {
				continue
			}
			seen[catID] = true
			// Используем category_id как имя — заменить на FSQ taxonomy при наличии.
			cats = append(cats, Category{ID: catID, Name: catID})
		}
	}
	return cats
}

func loadCategories(ctx context.Context, idx *vecdex.TypedIndex[Category], cats []Category) error {
	// Категории загружаем по одной — каждая вызывает embedder.
	for i, cat := range cats {
		if _, err := idx.Upsert(ctx, cat); err != nil {
			return fmt.Errorf("upsert category %s: %w", cat.ID, err)
		}
		if (i+1)%10 == 0 {
			log.Printf("categories: %d/%d", i+1, len(cats))
		}
	}
	log.Printf("categories: %d/%d done", len(cats), len(cats))
	return nil
}

func loadVenues(ctx context.Context, idx *vecdex.TypedIndex[Venue], places []fsqPlace) error {
	var batch []Venue
	loaded := 0

	for _, p := range places {
		catID := ""
		if len(p.CategoryIDs) > 0 {
			catID = p.CategoryIDs[0]
		}
		batch = append(batch, Venue{
			ID:    p.ID,
			Name:  p.Name,
			CatID: catID,
			Lat:   p.Lat,
			Lon:   p.Lon,
		})

		if len(batch) >= batchSize {
			if err := upsertBatch(ctx, idx, batch); err != nil {
				return err
			}
			loaded += len(batch)
			log.Printf("venues: %d/%d", loaded, len(places))
			batch = batch[:0]
		}
	}

	if len(batch) > 0 {
		if err := upsertBatch(ctx, idx, batch); err != nil {
			return err
		}
		loaded += len(batch)
	}
	log.Printf("venues: %d/%d done", loaded, len(places))
	return nil
}

func upsertBatch(ctx context.Context, idx *vecdex.TypedIndex[Venue], batch []Venue) error {
	resp, err := idx.UpsertBatch(ctx, batch)
	if err != nil {
		return fmt.Errorf("batch upsert: %w", err)
	}
	for _, r := range resp.Results {
		if !r.OK {
			log.Printf("warning: failed to upsert %s: %v", r.ID, r.Err)
		}
	}
	return nil
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
