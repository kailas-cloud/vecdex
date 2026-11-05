package vecdex

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"
)

func TestSDKOpaqueVersionIntegration(t *testing.T) {
	if testing.Short() ||
		(os.Getenv("VECDEX_E2E_VALKEY_CLUSTER_ADDRS") == "" && os.Getenv("VECDEX_E2E_VALKEY_ADDR") == "") {
		t.Skip("isolated Search fixture required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	client, err := newIntegrationClient(ctx, resolveIntegrationValkeyAddr())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	name := createIntegrationCollection(t, ctx, client)
	defer func() {
		cleanup, done := context.WithTimeout(context.Background(), 15*time.Second)
		defer done()
		if err := client.Collections().Delete(cleanup, name); err != nil {
			t.Error(err)
		}
	}()
	docs := client.Documents(name)
	first, second := writeVersionedSource(t, ctx, docs)
	assertSDKVersionRecreation(t, ctx, docs, first, second)
}

func writeVersionedSource(t *testing.T, ctx context.Context, docs *DocumentService) (first, second Document) {
	t.Helper()
	if _, err := docs.Upsert(ctx, Document{ID: "source", Content: "SciFact original"}); err != nil {
		t.Fatal(err)
	}
	first, err := docs.Get(ctx, "source")
	if err != nil || first.Version == "" {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	if _, err := docs.Upsert(
		ctx,
		Document{ID: "source", Content: "SciFact updated"},
		IfVersion(first.Version),
	); err != nil {
		t.Fatal(err)
	}
	second, err = docs.Get(ctx, "source")
	if err != nil || second.Version == first.Version {
		t.Fatalf("second=%+v err=%v", second, err)
	}

	return first, second
}

func assertSDKVersionRecreation(t *testing.T, ctx context.Context, docs *DocumentService, first, second Document) {
	t.Helper()
	var err error
	value := "stale"
	_, err = docs.Patch(ctx, "source", DocumentPatch{Content: &value}, IfVersion(first.Version))
	var conflict *VersionConflictError
	if !errors.Is(err, ErrRevisionConflict) || !errors.As(err, &conflict) || conflict.CurrentVersion != second.Version {
		t.Fatalf("stale patch: %v", err)
	}
	if err := docs.Delete(ctx, "source", IfVersion(first.Version)); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("stale delete: %v", err)
	}
	if err := docs.Delete(ctx, "source", IfVersion(second.Version)); err != nil {
		t.Fatal(err)
	}
	if _, err := docs.Upsert(ctx, Document{ID: "source", Content: "SciFact recreated"}); err != nil {
		t.Fatal(err)
	}
	recreated, err := docs.Get(ctx, "source")
	if err != nil || recreated.Version == first.Version || recreated.Version == second.Version {
		t.Fatalf("recreated=%+v err=%v", recreated, err)
	}
	if err := docs.Delete(ctx, "source", IfVersion(first.Version)); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("stale version crossed recreation: %v", err)
	}
}
