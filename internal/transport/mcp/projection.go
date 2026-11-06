package mcp

import (
	"encoding/json"
	"fmt"

	domdoc "github.com/kailas-cloud/vecdex/internal/domain/document"
)

func parseSourceExpiry(raw json.RawMessage) (*int64, error) {
	if raw == nil {
		return nil, nil //nolint:nilnil // Nil explicitly preserves omitted source expiry.
	}
	var value int64
	if string(raw) != "null" {
		if err := json.Unmarshal(raw, &value); err != nil {
			return nil, invalid("expires_at must be UTC Unix milliseconds or null")
		}
	}
	if value < 0 || value > domdoc.MaxExpiresAt {
		return nil, invalid("expires_at is outside the supported range")
	}
	return &value, nil
}
func projectedDocument(d *domdoc.Document, collection, generation string, a *sourceArgs) (map[string]any, error) {
	out := documentInfo(d, collection, generation, a.IncludeMetadata || len(a.MetadataFields) > 0)
	if len(a.MetadataFields) == 0 {
		return out, nil
	}
	var all map[string]json.RawMessage
	if err := json.Unmarshal(d.Metadata().JSON(), &all); err != nil {
		return nil, fmt.Errorf("decode source metadata projection: %w", err)
	}
	selected := map[string]json.RawMessage{}
	for _, name := range a.MetadataFields {
		if value, ok := all[name]; ok {
			selected[name] = value
		}
	}
	out["metadata"] = selected
	return out, nil
}
