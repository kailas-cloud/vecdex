// Package vecdex provides a Go client for the vecdex vector index service
// backed by Valkey or Redis with search modules.
//
// Vecdex supports two collection types:
//   - Text collections with embedding-based semantic search
//   - Geo collections with ECEF-based geographic proximity search
//
// # Low-level API — explicit control
//
//	client, _ := vecdex.New(ctx, vecdex.WithValkey("localhost:6379", ""))
//	defer client.Close()
//
//	client.Collections().Create(ctx, "places", vecdex.Geo(),
//	    vecdex.WithField("country", vecdex.FieldTag),
//	)
//	client.Documents("places").BatchUpsert(ctx, docs)
//	resp, _ := client.Search("places").Geo(ctx, 55.75, 37.62, 10, nil)
//	for _, r := range resp.Results { ... }
//
// # High-level API — schema-first with Go generics
//
//	type Place struct {
//	    ID      string  `vecdex:"id"`
//	    Name    string  `vecdex:"name,content"`
//	    Country string  `vecdex:"country,tag"`
//	    Lat     float64 `vecdex:"lat,geo_lat"`
//	    Lon     float64 `vecdex:"lon,geo_lon"`
//	}
//
//	idx, _ := vecdex.NewIndex[Place](client, "places")
//	_ = idx.Ensure(ctx)
//	_ = idx.UpsertBatch(ctx, places)
//	hits, _ := idx.Search().Near(55.75, 37.62).Km(10).Limit(50).Do(ctx)
//
// # Health and Usage
//
//	status := client.Health(ctx)
//	report := client.Usage(ctx, vecdex.PeriodDay)
package vecdex
