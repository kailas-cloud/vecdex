// Пример использования vecdex SDK для гео-поиска.
// Создаёт коллекцию мест на Кипре и ищет ближайшие к заданным координатам.
//
// Требует работающий Valkey на localhost:6379:
//
//	just valkey-up
//	go run ./examples/geo/
package main

import (
	"context"
	"fmt"
	"log"

	vecdex "github.com/kailas-cloud/vecdex/pkg/sdk"
)

// Place — типизированная модель для гео-индекса.
// Schema определяется через struct tags.
type Place struct {
	ID      string  `vecdex:"id,id"`
	Name    string  `vecdex:"name,tag"`
	Country string  `vecdex:"country,tag"`
	Lat     float64 `vecdex:"latitude,geo_lat"`
	Lon     float64 `vecdex:"longitude,geo_lon"`
}

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	ctx := context.Background()

	client, err := vecdex.New(ctx, vecdex.WithValkey("localhost:6379", ""))
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	defer client.Close()

	idx, err := vecdex.NewIndex[Place](client, "cyprus-places")
	if err != nil {
		return fmt.Errorf("new index: %w", err)
	}

	if err := idx.Ensure(ctx); err != nil {
		return fmt.Errorf("ensure: %w", err)
	}

	if err := seedPlaces(ctx, idx); err != nil {
		return err
	}

	return searchNearPaphos(ctx, idx)
}

func seedPlaces(ctx context.Context, idx *vecdex.TypedIndex[Place]) error {
	places := []Place{
		{ID: "paphos-castle", Name: "Paphos Castle", Country: "CY", Lat: 34.7533, Lon: 32.4069},
		{ID: "kourion", Name: "Kourion", Country: "CY", Lat: 34.6642, Lon: 32.8828},
		{ID: "tombs-of-kings", Name: "Tombs of the Kings", Country: "CY", Lat: 34.7736, Lon: 32.3967},
		{ID: "petra-tou-romiou", Name: "Petra tou Romiou", Country: "CY", Lat: 34.6631, Lon: 32.6275},
		{ID: "limassol-castle", Name: "Limassol Castle", Country: "CY", Lat: 34.6712, Lon: 33.0425},
	}

	results, err := idx.UpsertBatch(ctx, places)
	if err != nil {
		return fmt.Errorf("batch upsert: %w", err)
	}
	for _, r := range results {
		if !r.OK {
			log.Printf("failed to upsert %s: %v", r.ID, r.Err)
		}
	}
	return nil
}

func searchNearPaphos(ctx context.Context, idx *vecdex.TypedIndex[Place]) error {
	// Поиск ближайших мест к центру Пафоса (34.7720° N, 32.4246° E).
	hits, err := idx.Search().
		Near(34.7720, 32.4246).
		Km(30).
		Limit(10).
		Do(ctx)
	if err != nil {
		return fmt.Errorf("search: %w", err)
	}

	fmt.Printf("Found %d places within 30 km of Paphos:\n", len(hits))
	for _, h := range hits {
		fmt.Printf("  %.0fm  %s (%s)\n", h.Distance, h.Item.Name, h.Item.ID)
	}
	return nil
}
