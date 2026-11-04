package document

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/kailas-cloud/vecdex/internal/db"
	"github.com/kailas-cloud/vecdex/internal/domain"
	domdoc "github.com/kailas-cloud/vecdex/internal/domain/document"
)

type fragmentRow struct {
	End    int    `json:"end"`
	Vector string `json:"vector"`
}

func prepareFragmentFields(doc *domdoc.Document, base, version string) (map[string]string, []db.HashSetItem, error) {
	metadata, metadataErr := json.Marshal(db.RecordMetadata{Tags: doc.Tags(), Numerics: doc.Numerics()})
	if metadataErr != nil {
		return nil, nil, fmt.Errorf("encode metadata: %w: %w", domain.ErrInvalidSchema, metadataErr)
	}
	parts := doc.Fragments()
	if len(parts) == 0 {
		parts = []domdoc.Fragment{{Content: doc.Content(), Vector: doc.Vector()}}
	}
	rows := make([]fragmentRow, len(parts))
	children := make([]db.HashSetItem, len(parts))
	offset := 0
	for i, part := range parts {
		if part.Content == "" || !strings.HasPrefix(doc.Content()[offset:], part.Content) {
			return nil, nil, fmt.Errorf("fragment does not match source: %w", domain.ErrInvalidSchema)
		}
		if err := domain.ValidateVector(part.Vector, len(doc.Vector())); err != nil {
			return nil, nil, fmt.Errorf("fragment vector: %w", err)
		}
		offset += len(part.Content)
		rows[i] = fragmentRow{
			End:    offset,
			Vector: base64.StdEncoding.EncodeToString([]byte(vectorToBytes(part.Vector))),
		}
		child := buildHashFields(doc)
		child["__metadata"] = string(metadata)
		child["__content"] = part.Content
		child["__vector"] = vectorToBytes(part.Vector)
		child["__parent_id"] = doc.ID()
		child["__version"] = version
		child["__chunk_index"] = strconv.Itoa(i)
		children[i] = db.HashSetItem{Key: fmt.Sprintf("%s:chunk:%s:%s:%06d", base, doc.ID(), version, i), Fields: child}
	}
	if offset != len(doc.Content()) {
		return nil, nil, fmt.Errorf("incomplete fragments: %w", domain.ErrInvalidSchema)
	}
	encoded, err := json.Marshal(rows)
	if err != nil {
		return nil, nil, fmt.Errorf("encode fragments: %w", err)
	}
	fields := buildHashFields(doc)
	fields["__metadata"] = string(metadata)
	fields["__parts"] = string(encoded)
	fields["__chunk_version"] = version
	return fields, children, nil
}

func hydrateFragments(doc *domdoc.Document, fields map[string]string) error {
	if fields["__parts"] == "" {
		return nil
	}
	var rows []fragmentRow
	if err := json.Unmarshal([]byte(fields["__parts"]), &rows); err != nil {
		return fmt.Errorf("decode fragment manifest: %w", err)
	}
	parts := make([]domdoc.Fragment, len(rows))
	start := 0
	for i, row := range rows {
		if row.End <= start || row.End > len(doc.Content()) {
			return fmt.Errorf("invalid fragment span")
		}
		raw, err := base64.StdEncoding.DecodeString(row.Vector)
		if err != nil {
			return fmt.Errorf("decode fragment vector: %w", err)
		}
		vector := bytesToVector(string(raw))
		if err := domain.ValidateVector(vector, len(doc.Vector())); err != nil {
			return fmt.Errorf("stored fragment: %w", err)
		}
		parts[i] = domdoc.Fragment{Content: doc.Content()[start:row.End], Vector: vector}
		start = row.End
	}
	if start != len(doc.Content()) {
		return fmt.Errorf("incomplete stored fragments")
	}
	doc.SetFragments(parts)
	return nil
}
