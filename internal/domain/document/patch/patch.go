package patch

import (
	"encoding/json"
	"fmt"
	"unicode/utf8"

	"github.com/kailas-cloud/vecdex/internal/domain"
	"github.com/kailas-cloud/vecdex/internal/domain/collection/field"
)

// MaxContentSize is the maximum allowed content size in bytes.
const MaxContentSize = 163840 // 160KB

// Patch is a partial document update.
// Nil fields are unchanged. A nil value in Tags/Numerics means delete that field.
type Patch struct {
	metadata       json.RawMessage
	expiresAt      *int64
	metadataFields []field.Field
	content        *string
	tags           map[string]*string
	numerics       map[string]*float64
}

// New validates and creates a Patch. At least one field must be provided.
func New(content *string, tags map[string]*string, numerics map[string]*float64) (Patch, error) {
	if content == nil && len(tags) == 0 && len(numerics) == 0 {
		return Patch{}, fmt.Errorf("at least one field must be provided")
	}
	if err := validateContent(content); err != nil {
		return Patch{}, err
	}
	return Patch{content: content, tags: tags, numerics: numerics}, nil
}

// Content returns the new content, or nil if unchanged.
func (p Patch) Content() *string { return p.content }

// Tags returns tag updates (nil value = delete).
func (p Patch) Tags() map[string]*string { return p.tags }

// Numerics returns numeric updates (nil value = delete).
func (p Patch) Numerics() map[string]*float64 { return p.numerics }

// HasContent reports whether the patch includes a content change.
func (p Patch) HasContent() bool { return p.content != nil }

func validateContent(content *string) error {
	if content == nil {
		return nil
	}
	if !utf8.ValidString(*content) {
		return fmt.Errorf("content must be valid UTF-8: %w", domain.ErrInvalidSchema)
	}
	if len(*content) > MaxContentSize {
		return fmt.Errorf("content too large (maximum %d bytes): %w", MaxContentSize, domain.ErrInvalidSchema)
	}
	return nil
}
