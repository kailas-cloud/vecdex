// Package vecdex provides a Go client for the vecdex vector index service
// backed by Valkey or Redis with search modules.
//
// Vecdex manages text collections with automatic embeddings and
// semantic, keyword, or hybrid search.
//
// # Low-level API — explicit control
//
//	client, _ := vecdex.New(ctx, vecdex.WithValkey("localhost:6379", ""))
//	defer client.Close()
//
//	client.Collections().Create(ctx, "articles",
//	    vecdex.WithField("author", vecdex.FieldTag),
//	)
//	client.Documents("articles").BatchUpsert(ctx, docs)
//	resp, _ := client.Search("articles").Query(ctx, "redis vector search", &vecdex.SearchOptions{
//	    Mode:  vecdex.ModeHybrid,
//	    Limit: 10,
//	})
//	for _, r := range resp.Results { ... }
//
// # High-level API — schema-first with Go generics
//
//	type Article struct {
//	    ID      string `vecdex:"id,id"`
//	    Title   string `vecdex:"title,content"`
//	    Author  string `vecdex:"author,tag"`
//	    Year    int    `vecdex:"year,numeric"`
//	}
//
//	idx, _ := vecdex.NewIndex[Article](client, "articles")
//	_ = idx.Ensure(ctx)
//	_ = idx.UpsertBatch(ctx, articles)
//	hits, _ := idx.Search().Query("vector search").Mode(vecdex.ModeSemantic).Limit(10).Do(ctx)
//
// # Health and Usage
//
//	status := client.Health(ctx)
//	report := client.Usage(ctx, vecdex.PeriodDay)
package vecdex
