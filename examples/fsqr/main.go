// fsqr — семантический гео-поиск по местам Foursquare Open Places.
// Двухстадийный поиск: семантика по категориям → гео по venue'ам с фильтром.
//
// Env vars:
//   VALKEY_ADDR     — адрес Valkey (default: localhost:6379)
//   VALKEY_PASSWORD  — пароль Valkey
//   NEBIUS_API_KEY   — API ключ Nebius Inference
//   LISTEN           — HTTP listen addr (default: :8080)
package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"time"

	vecdex "github.com/kailas-cloud/vecdex/pkg/sdk"
)

// Venue — место из FSQ Open Places (гео-коллекция).
type Venue struct {
	ID    string  `vecdex:"id,id"`
	Name  string  `vecdex:"name,tag"`
	CatID string  `vecdex:"category_id,tag"`
	Lat   float64 `vecdex:"lat,geo_lat"`
	Lon   float64 `vecdex:"lon,geo_lon"`
}

// Category — категория FSQ (текстовая коллекция с эмбеддингами).
type Category struct {
	ID   string `vecdex:"id,id"`
	Name string `vecdex:"name,content"`
}

type searchRequest struct {
	Query string  `json:"query"`
	Lat   float64 `json:"lat"`
	Lon   float64 `json:"lon"`
	K     int     `json:"k"`
}

type searchResponse struct {
	Venues     []venueHit    `json:"venues"`
	Categories []categoryHit `json:"categories"`
	Meta       searchMeta    `json:"meta"`
}

type venueHit struct {
	ID       string  `json:"id"`
	Name     string  `json:"name"`
	CatID    string  `json:"category_id"`
	Lat      float64 `json:"lat"`
	Lon      float64 `json:"lon"`
	Distance float64 `json:"distance_m"`
}

type categoryHit struct {
	ID    string  `json:"id"`
	Name  string  `json:"name"`
	Score float64 `json:"score"`
}

type searchMeta struct {
	CategorySearchMs int64 `json:"category_search_ms"`
	VenueSearchMs    int64 `json:"venue_search_ms"`
	TotalMs          int64 `json:"total_ms"`
	VenueCount       int   `json:"venue_count"`
}

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	addr := env("VALKEY_ADDR", "localhost:6379")
	password := env("VALKEY_PASSWORD", "")
	apiKey := os.Getenv("NEBIUS_API_KEY")
	listen := env("LISTEN", ":8080")

	embedder := NewNebiusEmbedder(apiKey)

	client, err := vecdex.New(
		vecdex.WithValkey(addr, password),
		vecdex.WithEmbedder(embedder),
	)
	if err != nil {
		return err
	}
	defer client.Close()

	catIdx, err := vecdex.NewIndex[Category](client, "categories")
	if err != nil {
		return err
	}
	venueIdx, err := vecdex.NewIndex[Venue](client, "venues")
	if err != nil {
		return err
	}

	srv := &server{client: client, catIdx: catIdx, venueIdx: venueIdx}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /search", srv.handleSearch)
	mux.HandleFunc("GET /health", srv.handleHealth)

	log.Printf("fsqr listening on %s", listen)
	return http.ListenAndServe(listen, mux)
}

type server struct {
	client   *vecdex.Client
	catIdx   *vecdex.TypedIndex[Category]
	venueIdx *vecdex.TypedIndex[Venue]
}

func (s *server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if err := s.client.Ping(r.Context()); err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (s *server) handleSearch(w http.ResponseWriter, r *http.Request) {
	var req searchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.Query == "" {
		http.Error(w, "query required", http.StatusBadRequest)
		return
	}
	if req.K <= 0 {
		req.K = 10
	}

	ctx := r.Context()
	totalStart := time.Now()

	// Stage 1: семантический поиск по категориям.
	catStart := time.Now()
	catHits, err := s.catIdx.Search().
		Query(req.Query).
		Mode(vecdex.ModeSemantic).
		Limit(3).
		Do(ctx)
	if err != nil {
		http.Error(w, "category search: "+err.Error(), http.StatusInternalServerError)
		return
	}
	catMs := time.Since(catStart).Milliseconds()

	// Stage 2: гео-поиск по venue'ам с фильтром по найденным категориям.
	venueStart := time.Now()
	results, err := s.searchVenues(ctx, req, catHits)
	if err != nil {
		http.Error(w, "venue search: "+err.Error(), http.StatusInternalServerError)
		return
	}
	venueMs := time.Since(venueStart).Milliseconds()

	resp := buildResponse(catHits, results, catMs, venueMs, time.Since(totalStart).Milliseconds())

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func (s *server) searchVenues(
	ctx context.Context, req searchRequest, catHits []vecdex.Hit[Category],
) ([]vecdex.SearchResult, error) {
	// Should-фильтры: venue матчится, если category_id совпадает
	// хотя бы с одной из найденных категорий (OR-логика).
	var filters []vecdex.FilterCondition
	for _, h := range catHits {
		filters = append(filters, vecdex.FilterCondition{
			Key:   "category_id",
			Match: h.Item.ID,
		})
	}

	opts := vecdex.SearchOptions{
		Filters:  vecdex.FilterExpression{Should: filters},
		MinScore: 50_000, // радиус 50 км в метрах
	}

	return s.client.Search("venues").Geo(ctx, req.Lat, req.Lon, req.K, opts)
}

func buildResponse(
	catHits []vecdex.Hit[Category],
	venueHits []vecdex.SearchResult,
	catMs, venueMs, totalMs int64,
) searchResponse {
	categories := make([]categoryHit, len(catHits))
	for i, h := range catHits {
		categories[i] = categoryHit{
			ID:    h.Item.ID,
			Name:  h.Item.Name,
			Score: h.Score,
		}
	}

	venues := make([]venueHit, len(venueHits))
	for i, r := range venueHits {
		lat, _ := r.Numerics["lat"]
		lon, _ := r.Numerics["lon"]
		venues[i] = venueHit{
			ID:       r.ID,
			Name:     r.Tags["name"],
			CatID:    r.Tags["category_id"],
			Lat:      lat,
			Lon:      lon,
			Distance: r.Score, // гео-поиск: score = расстояние в метрах
		}
	}

	return searchResponse{
		Venues:     venues,
		Categories: categories,
		Meta: searchMeta{
			CategorySearchMs: catMs,
			VenueSearchMs:    venueMs,
			TotalMs:          totalMs,
			VenueCount:       len(venues),
		},
	}
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
