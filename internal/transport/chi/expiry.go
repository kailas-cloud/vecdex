package chi

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/kailas-cloud/vecdex/internal/domain"
	"github.com/kailas-cloud/vecdex/internal/domain/document"
)

func optionalExpiresAt(value int64) *int64 {
	if value == 0 {
		return nil
	}
	return &value
}

func expiryPatchFromJSON(raw json.RawMessage) (*int64, error) {
	if raw == nil {
		return nil, nil //nolint:nilnil // An omitted patch leaves the deadline unchanged.
	}
	var value int64
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return &value, nil
	}
	if err := json.Unmarshal(raw, &value); err != nil || value <= 0 || value > document.MaxExpiresAt {
		return nil, fmt.Errorf("expires_at must be UTC Unix milliseconds or null: %w", domain.ErrInvalidSchema)
	}
	return &value, nil
}
