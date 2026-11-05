package patch

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/kailas-cloud/vecdex/internal/domain/collection/field"
	"github.com/kailas-cloud/vecdex/internal/domain/metadata"
)

// NewMetadata creates a text/JSON metadata patch. Omitted metadata is unchanged;
// JSON null clears it, and object members follow JSON Merge Patch semantics.
func NewMetadata(content *string, raw json.RawMessage) (Patch, error) {
	if content == nil && raw == nil {
		return Patch{}, fmt.Errorf("at least one field must be provided")
	}
	if err := validateContent(content); err != nil {
		return Patch{}, err
	}
	if raw != nil && !bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		if _, err := metadata.Parse(raw); err != nil {
			return Patch{}, fmt.Errorf("metadata patch: %w", err)
		}
	}
	return Patch{content: content, metadata: slices.Clone(raw)}, nil
}

// Metadata returns the detached merge patch, or nil when unchanged.
func (p Patch) Metadata() json.RawMessage { return slices.Clone(p.metadata) }

// HasMetadata reports an explicit metadata update, including JSON null.
func (p Patch) HasMetadata() bool { return p.metadata != nil }

// WithMetadataFields binds projection to the collection schema already read.
func (p Patch) WithMetadataFields(fields []field.Field) Patch {
	p.metadataFields = slices.Clone(fields)
	return p
}

// MetadataFields returns the immutable indexed-field definitions.
func (p Patch) MetadataFields() []field.Field { return slices.Clone(p.metadataFields) }
