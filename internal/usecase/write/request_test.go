package write

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/kailas-cloud/vecdex/internal/domain"
	"github.com/kailas-cloud/vecdex/internal/domain/document"
	"github.com/kailas-cloud/vecdex/internal/domain/document/patch"
)

type recordingRepository struct {
	fingerprint string
}

func (r *recordingRepository) BeginWrite(
	ctx context.Context, _, _, fingerprint string,
) (context.Context, *document.WriteResult, error) {
	r.fingerprint = fingerprint
	return ctx, nil, nil
}

func (*recordingRepository) ReleaseWrite(context.Context) error { return nil }

func TestPatchFingerprintPreservesCallerIntent(t *testing.T) {
	text := "source text"
	persistent, future := int64(0), int64(1794000000000)
	variants := []struct {
		name, metadata, version string
		content                 *string
		expiresAt               *int64
		sameAs                  int
	}{
		{name: "content only", content: &text, sameAs: -1},
		{name: "clear metadata", content: &text, metadata: "null", sameAs: -1},
		{name: "empty merge", content: &text, metadata: "{}", sameAs: -1},
		{name: "nested exact integer", metadata: `{"b":{"n":9007199254740993},"a":true}`, sameAs: -1},
		{name: "reordered object", metadata: ` { "a": true, "b": { "n":9007199254740993 }} `, sameAs: 3},
		{name: "different exact integer", metadata: `{"b":{"n":9007199254740992},"a":true}`, sameAs: -1},
		{name: "conditional", content: &text, version: "opaque-version", sameAs: -1},
		{name: "clear deadline", content: &text, expiresAt: &persistent, sameAs: -1},
		{name: "set deadline", content: &text, expiresAt: &future, sameAs: -1},
	}
	fingerprints := make([]string, len(variants))
	for i, variant := range variants {
		t.Run(variant.name, func(t *testing.T) {
			var raw json.RawMessage
			if variant.metadata != "" {
				raw = json.RawMessage(variant.metadata)
			}
			p, err := patch.NewSource(variant.content, raw, variant.expiresAt)
			if err != nil {
				t.Fatal(err)
			}
			ctx := domain.WithIdempotencyKey(context.Background(), "operation")
			if variant.version != "" {
				ctx = domain.WithExpectedVersion(ctx, &variant.version)
			}
			repo := &recordingRepository{}
			_, _, done, err := Patch(ctx, repo, "notes", "source", p)
			if err != nil {
				t.Fatal(err)
			}
			done()
			fingerprints[i] = repo.fingerprint
			for previous, fingerprint := range fingerprints[:i] {
				if (fingerprint == repo.fingerprint) != (previous == variant.sameAs) {
					t.Fatalf("caller intent collision with %s", variants[previous].name)
				}
			}
		})
	}
}
