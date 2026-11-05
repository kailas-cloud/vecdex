package patch

import (
	"fmt"

	"github.com/kailas-cloud/vecdex/internal/domain"
	"github.com/kailas-cloud/vecdex/internal/domain/document"
)

// ExpiresAt distinguishes an omitted deadline from an explicit zero (clear).
func (p Patch) ExpiresAt() *int64 {
	if p.expiresAt == nil {
		return nil
	}
	value := *p.expiresAt
	return &value
}

// NewSource creates a source patch. Nil metadata/deadline fields are unchanged;
// an explicit zero deadline makes the source persistent.
func NewSource(content *string, raw []byte, expiresAt *int64) (Patch, error) {
	if expiresAt == nil {
		return NewMetadata(content, raw)
	}
	if *expiresAt < 0 || *expiresAt > document.MaxExpiresAt {
		return Patch{}, fmt.Errorf("invalid expires_at: %w", domain.ErrInvalidSchema)
	}
	var p Patch
	var err error
	if content != nil || raw != nil {
		p, err = NewMetadata(content, raw)
		if err != nil {
			return Patch{}, err
		}
	}
	value := *expiresAt
	p.expiresAt = &value
	return p, nil
}
