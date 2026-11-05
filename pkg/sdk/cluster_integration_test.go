package vecdex

import (
	"context"
	"fmt"
	"os"
	"slices"
	"testing"
	"time"
)

func TestDistributedCollectionSearchIntegration(t *testing.T) {
	if os.Getenv("VECDEX_E2E_VALKEY_CLUSTER_ADDRS") == "" {
		t.Skip("isolated Valkey Search cluster required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	client, err := newIntegrationClient(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	name := createIntegrationCollection(t, ctx, client)
	defer func() {
		if err := client.Collections().Delete(context.Background(), name); err != nil {
			t.Error(err)
		}
	}()
	docs := make([]BatchUpsertItem, 64)
	want := make([]string, len(docs))
	for i := range docs {
		want[i] = fmt.Sprintf("source-%03d", i)
		docs[i] = BatchUpsertItem{ID: want[i], Content: "SciFact distributed memory source " + want[i]}
	}
	response, err := client.Documents(name).BatchUpsert(ctx, docs)
	if err != nil || response.Succeeded != len(docs) {
		t.Fatalf("batch=%+v err=%v", response, err)
	}
	if count, err := client.Documents(name).Count(ctx); err != nil || count != len(docs) {
		t.Fatalf("count=%d err=%v", count, err)
	}
	var got []string
	cursor := ""
	for {
		page, err := client.Documents(name).List(ctx, cursor, 17)
		if err != nil {
			t.Fatal(err)
		}
		for _, doc := range page.Documents {
			got = append(got, doc.ID)
		}
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
	}
	if !slices.Equal(got, want) {
		t.Fatal("partition pagination lost source documents")
	}
	for _, mode := range []SearchMode{ModeKeyword, ModeSemantic, ModeHybrid} {
		waitDistributedSearch(t, ctx, client, name, mode, want)
	}
}

func waitDistributedSearch(
	t *testing.T, ctx context.Context, client *Client, name string, mode SearchMode, want []string,
) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		response, err := client.Search(name).Query(ctx, "SciFact", &SearchOptions{Mode: mode, Limit: 100})
		if err == nil && len(response.Results) == len(want) {
			ids := make([]string, len(response.Results))
			for i, hit := range response.Results {
				ids[i] = hit.ID
			}
			slices.Sort(ids)
			if !slices.Equal(ids, want) {
				t.Fatalf("%s returned incorrect source IDs", mode)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s search did not converge: results=%d err=%v", mode, len(response.Results), err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// Repeated creation/write/search cycles expose remote text-content resolution
// failures that a single already-warmed index can hide.
func TestClusterFreshTextSearchIntegration(t *testing.T) {
	if testing.Short() || os.Getenv("VECDEX_E2E_VALKEY_CLUSTER_ADDRS") == "" {
		t.Skip("isolated Search cluster required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	client, err := newIntegrationClient(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	for iteration := range 4 {
		t.Run(fmt.Sprintf("fresh-%d", iteration), func(t *testing.T) {
			name := fmt.Sprintf("sdk_fresh_%d", time.Now().UnixNano())
			if _, err := client.Collections().Ensure(ctx, name, WithField("scope", FieldTag)); err != nil {
				t.Fatal(err)
			}
			defer func() {
				cleanup, done := context.WithTimeout(context.Background(), 15*time.Second)
				defer done()
				if err := client.Collections().Delete(cleanup, name); err != nil {
					t.Error(err)
				}
			}()
			want := seedTopologyDocuments(t, ctx, client, name)
			assertTopologySearch(t, ctx, client, name, want)
		})
	}
}
