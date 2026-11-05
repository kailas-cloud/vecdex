package document

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/kailas-cloud/vecdex/internal/db"
	"github.com/kailas-cloud/vecdex/internal/domain"
	domdoc "github.com/kailas-cloud/vecdex/internal/domain/document"
	"github.com/kailas-cloud/vecdex/internal/domain/storage"
)

type fragmentRow struct {
	End int `json:"end"`
}

func prepareFragmentFields(
	doc *domdoc.Document, layout storage.Collection, version string,
) (map[string]string, []db.HashSetItem, error) {
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
		if !matchesFragment(doc.Content()[offset:], part.Content) {
			return nil, nil, fmt.Errorf("fragment does not match source: %w", domain.ErrInvalidSchema)
		}
		if err := domain.ValidateVector(part.Vector, len(doc.Vector())); err != nil {
			return nil, nil, fmt.Errorf("fragment vector: %w", err)
		}
		offset += len(part.Content)
		rows[i] = fragmentRow{End: offset}
		child := buildHashFields(doc)
		child["__metadata"] = string(metadata)
		child["__content"] = part.Content
		child["__vector"] = vectorToBytes(part.Vector)
		child["__parent_id"] = doc.ID()
		child["__version"] = version
		child["__chunk_index"] = strconv.Itoa(i)
		children[i] = db.HashSetItem{Key: layout.Fragment(doc.ID(), version, i), Fields: child}
	}
	if offset != len(doc.Content()) {
		return nil, nil, fmt.Errorf("incomplete fragments: %w", domain.ErrInvalidSchema)
	}
	encoded, err := json.Marshal(rows)
	if err != nil {
		return nil, nil, fmt.Errorf("encode fragments: %w", err)
	}
	fields := buildHashFields(doc)
	delete(fields, "__vector")
	fields["__vector_dim"] = strconv.Itoa(len(doc.Vector()))
	fields["__metadata"] = string(metadata)
	if doc.Metadata().Present() {
		fields["__source_metadata"] = string(doc.Metadata().JSON())
	}
	fields["__parts"] = string(encoded)
	fields["__chunk_version"] = version
	return fields, children, nil
}

func matchesFragment(remaining, content string) bool {
	return content != "" && strings.HasPrefix(remaining, content)
}

func hydrateFragments(doc *domdoc.Document, fields map[string]string, vectors []string) error {
	if fields["__parts"] == "" {
		return nil
	}
	var rows []fragmentRow
	if err := json.Unmarshal([]byte(fields["__parts"]), &rows); err != nil {
		return fmt.Errorf("decode fragment manifest: %w", err)
	}
	dimensions, err := strconv.Atoi(fields["__vector_dim"])
	if err != nil || dimensions <= 0 || len(vectors) != len(rows) {
		return fmt.Errorf("invalid fragment vector manifest")
	}
	parts := make([]domdoc.Fragment, len(rows))
	start := 0
	for i, row := range rows {
		if row.End <= start || row.End > len(doc.Content()) {
			return fmt.Errorf("invalid fragment span")
		}
		vector := bytesToVector(vectors[i])
		err := domain.ValidateVector(vector, dimensions)
		if err != nil {
			return fmt.Errorf("stored fragment vector: %w", err)
		}
		parts[i] = domdoc.Fragment{Content: doc.Content()[start:row.End], Vector: vector}
		start = row.End
	}
	if start != len(doc.Content()) {
		return fmt.Errorf("incomplete stored fragments")
	}
	doc.SetFragments(parts)
	if len(parts) > 0 {
		doc.SetVector(domdoc.RepresentativeVector(parts))
	}
	return nil
}
