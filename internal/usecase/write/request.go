// Package write coordinates source request claims before expensive preparation.
package write

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/kailas-cloud/vecdex/internal/domain"
	"github.com/kailas-cloud/vecdex/internal/domain/document"
	"github.com/kailas-cloud/vecdex/internal/domain/document/patch"
)

type repository interface {
	BeginWrite(ctx context.Context, collection, id, fingerprint string) (context.Context, *document.WriteResult, error)
	ReleaseWrite(ctx context.Context) error
}

type input struct {
	Content         *string         `json:"content"`
	Metadata        json.RawMessage `json:"metadata"`
	MetadataPresent bool            `json:"metadata_present"`
	Tags            any             `json:"tags,omitempty"`
	Numerics        any             `json:"numerics,omitempty"`
	Expected        *string         `json:"expected"`
	Action          string          `json:"action"`
	ExpiresAt       *int64          `json:"expires_at"`
	RequireAbsent   bool            `json:"require_absent,omitempty"`
}

// Upsert reserves the exact caller input, independently of vector preparation.
func Upsert(
	ctx context.Context, repo any, collection string, doc *document.Document,
) (context.Context, *document.WriteResult, func(), error) {
	content := doc.Content()
	expiresAt := doc.ExpiresAt()
	return begin(ctx, repo, collection, doc.ID(), &input{Content: &content, Metadata: doc.Metadata().JSON(),
		Tags: doc.Tags(), Numerics: doc.Numerics(), Expected: domain.ExpectedVersion(ctx),
		Action: "upsert", ExpiresAt: &expiresAt, RequireAbsent: domain.RequireAbsent(ctx)})
}

// Patch fingerprints the caller's merge patch, before any internal CAS retry.
func Patch(
	ctx context.Context, repo any, collection, id string, p patch.Patch,
) (context.Context, *document.WriteResult, func(), error) {
	raw, err := canonicalMetadata(p.Metadata())
	if err != nil {
		return ctx, nil, func() {}, err
	}
	return begin(ctx, repo, collection, id, &input{Content: p.Content(), Metadata: raw, MetadataPresent: p.HasMetadata(),
		Tags: p.Tags(), Numerics: p.Numerics(), Expected: domain.ExpectedVersion(ctx),
		Action: "patch", ExpiresAt: p.ExpiresAt()})
}

// Delete reserves an outcome even when a later retry finds the source absent.
func Delete(
	ctx context.Context, repo any, collection, id string,
) (context.Context, *document.WriteResult, func(), error) {
	return begin(ctx, repo, collection, id, &input{Expected: domain.ExpectedVersion(ctx), Action: "delete"})
}

func begin(
	ctx context.Context, repo any, collection, id string, value *input,
) (context.Context, *document.WriteResult, func(), error) {
	done := func() {}
	key := domain.IdempotencyKey(ctx)
	if key == "" {
		return ctx, nil, done, nil
	}
	if err := domain.ValidateIdempotencyKey(key); err != nil {
		return ctx, nil, done, fmt.Errorf("source request key: %w", err)
	}
	backend, ok := repo.(repository)
	if !ok {
		return ctx, nil, done, domain.ErrNotImplemented
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return ctx, nil, done, fmt.Errorf("encode source request: %w", err)
	}
	digest := sha256.Sum256(encoded)
	claimed, replay, err := backend.BeginWrite(ctx, collection, id, hex.EncodeToString(digest[:]))
	if err != nil {
		return ctx, nil, done, fmt.Errorf("begin source request: %w", err)
	}
	if replay != nil {
		domain.MarkWriteReplayed(ctx)
		return ctx, replay, done, nil
	}
	done = func() {
		release, cancel := context.WithTimeout(context.WithoutCancel(claimed), 2*time.Second)
		defer cancel()
		_ = backend.ReleaseWrite(release)
	}
	return claimed, nil, done, nil
}

func canonicalMetadata(raw json.RawMessage) (json.RawMessage, error) {
	if raw == nil {
		return nil, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, fmt.Errorf("decode source request metadata: %w", err)
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("encode source request metadata: %w", err)
	}
	return encoded, nil
}
