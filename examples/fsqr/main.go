// fsqr — семантический гео-поиск по местам Foursquare Open Places.
// Двухстадийный поиск: семантика по категориям → гео по venue'ам с фильтром.
//
// Env vars:
//
//	VALKEY_ADDR     — адрес Valkey (default: localhost:6379)
//	VALKEY_PASSWORD — пароль Valkey
//	NEBIUS_API_KEY  — API ключ Nebius Inference
//	LISTEN          — HTTP listen addr (default: :8080)
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"time"

	vecdex "github.com/kailas-cloud/vecdex/pkg/sdk"
)

// Venue — место из FSQ Open Places (гео-коллекция).
type Venue struct {
	ID   string  `vecdex:"id,id"`
	Name string  `vecdex:"name,tag"`
	Cat  int     `vecdex:"cat,numeric"`
	Lat  float64 `vecdex:"latitude,geo_lat"`
	Lon  float64 `vecdex:"longitude,geo_lon"`
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
	Cat      int     `json:"cat"`
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

	ctx := context.Background()
	client, err := vecdex.New(
		ctx,
		vecdex.WithRedis(addr, password),
		vecdex.WithEmbedder(embedder),
		vecdex.WithVectorDimensions(4096),
	)
	if err != nil {
		return fmt.Errorf("init vecdex: %w", err)
	}
	defer client.Close()

	catIdx, err := vecdex.NewIndex[Category](client, "fsqr-categories")
	if err != nil {
		return fmt.Errorf("init categories index: %w", err)
	}
	venueIdx, err := vecdex.NewIndex[Venue](client, "fsqr-venues")
	if err != nil {
		return fmt.Errorf("init venues index: %w", err)
	}

	srv := &server{client: client, catIdx: catIdx, venueIdx: venueIdx}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /search", srv.handleSearch)
	mux.HandleFunc("GET /health", srv.handleHealth)

	log.Printf("fsqr listening on %s", listen)
	httpSrv := &http.Server{
		Addr:         listen,
		Handler:      mux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
	}
	if err := httpSrv.ListenAndServe(); err != nil {
		return fmt.Errorf("http server: %w", err)
	}
	return nil
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
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		log.Printf("encode response: %v", err)
	}
}

func (s *server) searchVenues(
	ctx context.Context, req searchRequest, catHits []vecdex.Hit[Category],
) ([]vecdex.SearchResult, error) {
	// Should-фильтры: venue матчится, если cat совпадает
	// хотя бы с одной из найденных категорий (OR-логика).
	// Без ограничения по расстоянию — KNN вернёт K ближайших где бы они ни были.
	filters := make([]vecdex.FilterCondition, 0, len(catHits))
	for _, h := range catHits {
		catID, err := strconv.ParseFloat(h.Item.ID, 64)
		if err != nil {
			continue
		}
		filters = append(filters, vecdex.FilterCondition{
			Key:   "cat",
			Range: &vecdex.RangeFilter{GTE: &catID, LTE: &catID},
		})
	}

	opts := vecdex.SearchOptions{
		Filters: vecdex.FilterExpression{Should: filters},
	}

	resp, err := s.client.Search("fsqr-venues").Geo(ctx, req.Lat, req.Lon, req.K, &opts)
	if err != nil {
		return nil, fmt.Errorf("geo search: %w", err)
	}
	return resp.Results, nil
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
		venues[i] = venueHit{
			ID:       r.ID,
			Name:     r.Tags["name"],
			Cat:      int(r.Numerics["cat"]),
			Lat:      r.Numerics["latitude"],
			Lon:      r.Numerics["longitude"],
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
