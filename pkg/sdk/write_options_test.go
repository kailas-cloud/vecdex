package vecdex

import (
	"context"
	"errors"
	"testing"

	"github.com/kailas-cloud/vecdex/internal/domain"
	domdoc "github.com/kailas-cloud/vecdex/internal/domain/document"
	"github.com/kailas-cloud/vecdex/internal/domain/document/patch"
)

func TestIfAbsentCarriesAtomicConditionAndRejectsVersion(t *testing.T) {
	ctx, err := applyWriteOptions(context.Background(), []WriteOption{IfAbsent(), IdempotencyKey("create")})
	if err != nil || !domain.RequireAbsent(ctx) || domain.IdempotencyKey(ctx) != "create" {
		t.Fatalf("create-only option: %v", err)
	}
	_, err = applyWriteOptions(ctx, []WriteOption{IfAbsent(), IfVersion("version")})
	if !errors.Is(err, ErrInvalidSchema) {
		t.Fatalf("contradictory preconditions: %v", err)
	}
	ctx, err = applyWriteOptions(ctx, nil)
	if err != nil || domain.RequireAbsent(ctx) {
		t.Fatalf("ambient create-only leaked: %v", err)
	}
}

func TestDocumentWritesCarryConditionAndRequestKey(t *testing.T) {
	require := func(ctx context.Context) {
		t.Helper()
		version := domain.ExpectedVersion(ctx)
		if version == nil || *version != "opaque-version" {
			t.Fatal("write condition was lost")
		}
		if domain.IdempotencyKey(ctx) != "operation-key" {
			t.Fatal("source request key was lost")
		}
	}
	source, _ := domdoc.New("source", "text", nil, nil)
	source.SetVersion("next-version")
	uc := &mockDocumentUC{
		upsertFn: func(ctx context.Context, _ string, _ *domdoc.Document) (bool, error) { require(ctx); return false, nil },
		deleteFn: func(ctx context.Context, _, _ string) error { require(ctx); return nil },
		patchFn: func(ctx context.Context, _, _ string, _ patch.Patch) (domdoc.WriteResult, error) {
			require(ctx)
			return domdoc.WriteResult{ID: source.ID(), Version: source.Version()}, nil
		},
		getFn: func(context.Context, string, string) (domdoc.Document, error) { return source, nil },
	}
	service := newDocSvc(uc, nil)
	if _, err := service.Upsert(
		context.Background(),
		Document{ID: "source", Content: "text"},
		IfVersion("opaque-version"),
		IdempotencyKey("operation-key"),
	); err != nil {
		t.Fatal(err)
	}
	if err := service.Delete(context.Background(), "source",
		IfVersion("opaque-version"), IdempotencyKey("operation-key")); err != nil {
		t.Fatal(err)
	}
	value := "new text"
	patched, err := service.Patch(
		context.Background(),
		"source",
		DocumentPatch{Content: &value},
		IfVersion("opaque-version"),
		IdempotencyKey("operation-key"),
	)
	if err != nil || patched.Version != "next-version" {
		t.Fatalf("patch=%+v err=%v", patched, err)
	}
	got, err := service.Get(context.Background(), "source")
	if err != nil || got.Version != "next-version" {
		t.Fatalf("get=%+v err=%v", got, err)
	}
}

func TestWriteWithoutOptionsDoesNotInheritAnotherRequest(t *testing.T) {
	version := "another-version"
	ctx := domain.WithIdempotencyKey(domain.WithExpectedVersion(context.Background(), &version), "another-key")
	actual, err := applyWriteOptions(ctx, nil)
	if err != nil || domain.ExpectedVersion(actual) != nil || domain.IdempotencyKey(actual) != "" {
		t.Fatalf("an ambient source request leaked into a new SDK call: %v", err)
	}
}

func TestEmptyVersionCannotBecomeUnconditionalWrite(t *testing.T) {
	service := newDocSvc(&mockDocumentUC{}, nil)
	_, putErr := service.Upsert(context.Background(), Document{ID: "source", Content: "text"}, IfVersion(""))
	deleteErr := service.Delete(context.Background(), "source", IfVersion(""))
	_, patchErr := service.Patch(context.Background(), "source", DocumentPatch{}, IfVersion(""))
	for _, err := range []error{putErr, deleteErr, patchErr} {
		if !errors.Is(err, ErrInvalidSchema) {
			t.Fatalf("empty condition reached storage: %v", err)
		}
	}
}
