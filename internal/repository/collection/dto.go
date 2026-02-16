package collection

import (
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/kailas-cloud/vecdex/internal/domain/collection"
	"github.com/kailas-cloud/vecdex/internal/domain/collection/field"
)

// fieldRow is the JSON-serializable representation of a field for HSET.
type fieldRow struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

// collectionToHash converts a domain Collection to a map for HSET.
func collectionToHash(col collection.Collection) (map[string]string, error) {
	rows := make([]fieldRow, len(col.Fields()))
	for i, f := range col.Fields() {
		rows[i] = fieldRow{Name: f.Name(), Type: string(f.FieldType())}
	}
	fieldsJSON, err := json.Marshal(rows)
	if err != nil {
		return nil, fmt.Errorf("marshal fields: %w", err)
	}
	return map[string]string{
		"name":            col.Name(),
		"type":            "json",
		"collection_type": string(col.Type()),
		"fields_json":     string(fieldsJSON),
		"vector_dim":      strconv.Itoa(col.VectorDim()),
		"created_at":      strconv.FormatInt(col.CreatedAt(), 10),
		"revision":        strconv.Itoa(col.Revision()),
	}, nil
}

// collectionFromHash hydrates a domain Collection from an HGETALL result map.
func collectionFromHash(m map[string]string, defaultVectorDim int) (collection.Collection, error) {
	name := m["name"]
	createdAtStr := m["created_at"]
	fieldsJSON := m["fields_json"]

	createdAt, err := strconv.ParseInt(createdAtStr, 10, 64)
	if err != nil {
		return collection.Collection{}, fmt.Errorf("invalid created_at: %w", err)
	}

	var rows []fieldRow
	if fieldsJSON != "" {
		if err := json.Unmarshal([]byte(fieldsJSON), &rows); err != nil {
			return collection.Collection{}, fmt.Errorf("unmarshal fields: %w", err)
		}
	}

	fields := make([]field.Field, len(rows))
	for i, r := range rows {
		fields[i] = field.Reconstruct(r.Name, field.Type(r.Type))
	}

	vectorDim := defaultVectorDim
	if dimStr, ok := m["vector_dim"]; ok && dimStr != "" {
		if parsed, err := strconv.Atoi(dimStr); err == nil {
			vectorDim = parsed
		}
	}

	revision := 1
	if revStr, ok := m["revision"]; ok && revStr != "" {
		if parsed, err := strconv.Atoi(revStr); err == nil {
			revision = parsed
		}
	}

	colType := collection.Type(m["collection_type"])
	return collection.Reconstruct(name, colType, fields, vectorDim, createdAt, revision), nil
}
