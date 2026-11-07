package vecdex

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/kailas-cloud/vecdex/internal/domain"
)

func TestAgentSourceConditionsIntegration(t *testing.T) {
	requireJSONMetadataFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	client, _ := newMetadataIntegrationClient(t, ctx)
	defer client.Close()
	name := fmt.Sprintf("agent_contract_%d", time.Now().UnixNano())
	original, err := client.Collections().Ensure(ctx, name)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Collections().Delete(ctx, name) }()
	fenced := WithCollectionGeneration(ctx, original.Generation)
	docs := client.Documents(name)
	source := Document{ID: "source", Content: "SciFact source evidence", Metadata: map[string]any{"kind": "note"}}
	version := assertAgentCreateOnly(t, fenced, docs, source)
	assertAgentProjection(t, fenced, docs, source, version)
	assertAgentStaleGeneration(t, ctx, client, &original, source)
}

func assertAgentCreateOnly(t *testing.T, ctx context.Context, docs *DocumentService, source Document) string {
	t.Helper()
	firstCtx, firstOutcome := domain.WithWriteOutcome(ctx)
	first, err := docs.Upsert(firstCtx, source, IfAbsent(), IdempotencyKey("create-source"))
	if err != nil || !first.Created || firstOutcome.Replayed() {
		t.Fatalf("create-only first result: %+v replay=%v err=%v", first, firstOutcome.Replayed(), err)
	}
	_, err = docs.Upsert(ctx, source, IfAbsent(), IdempotencyKey("another-create"))
	if !errors.Is(err, ErrAlreadyExists) {
		t.Fatalf("create-only replaced an existing source: %v", err)
	}
	replayCtx, replayOutcome := domain.WithWriteOutcome(ctx)
	again, err := docs.Upsert(replayCtx, source, IfAbsent(), IdempotencyKey("create-source"))
	if err != nil || again != first || !replayOutcome.Replayed() {
		t.Fatalf("create-only receipt replay: %+v replay=%v err=%v", again, replayOutcome.Replayed(), err)
	}
	return first.Version
}

func assertAgentProjection(t *testing.T, ctx context.Context, docs *DocumentService, source Document, version string) {
	t.Helper()
	projected, err := docs.ReadSource(ctx, source.ID, SourceProjection{Metadata: true})
	if err != nil || projected.Content != "" || projected.Version != version {
		t.Fatalf("metadata projection: %+v %v", projected, err)
	}
	requireSameMetadata(t, projected.Metadata, source.Metadata)
	page, err := docs.ListSources(ctx, "", 1, SourceProjection{})
	if err != nil || len(page.Documents) != 1 || page.Documents[0].Content != "" ||
		len(page.Documents[0].Metadata) != 0 {
		t.Fatalf("identity-only projection: %+v %v", page, err)
	}
}

func assertAgentStaleGeneration(
	t *testing.T, ctx context.Context, c *Client, original *CollectionInfo, source Document,
) {
	t.Helper()
	name := original.Name
	if err := c.Collections().DeleteIfGeneration(ctx, name, original.Generation); err != nil {
		t.Fatal(err)
	}
	fresh, err := c.Collections().Ensure(ctx, name)
	if err != nil || fresh.Generation == original.Generation {
		t.Fatalf("recreation identity: %+v %v", fresh, err)
	}
	fenced := WithCollectionGeneration(ctx, original.Generation)
	docs := c.Documents(name)
	_, err = docs.ReadSource(fenced, source.ID, SourceProjection{})
	if !errors.Is(err, ErrCollectionGenerationMismatch) {
		t.Fatalf("stale context read crossed collection recreation: %v", err)
	}
	_, err = docs.Upsert(fenced, source, IfAbsent(), IdempotencyKey("create-source"))
	if !errors.Is(err, ErrCollectionGenerationMismatch) {
		t.Fatalf("stale context write crossed collection recreation: %v", err)
	}
	err = c.Collections().DeleteIfGeneration(ctx, name, original.Generation)
	if !errors.Is(err, ErrCollectionGenerationMismatch) {
		t.Fatalf("stale collection delete: %v", err)
	}
	inspection, err := c.Collections().Inspect(ctx, name)
	if err != nil || inspection.State != "active" || inspection.Generation != fresh.Generation {
		t.Fatalf("new generation damaged by stale operations: %+v %v", inspection, err)
	}
}
