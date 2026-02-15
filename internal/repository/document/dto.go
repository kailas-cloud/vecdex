package document

import (
	"encoding/json"
	"fmt"

	domdoc "github.com/kailas-cloud/vecdex/internal/domain/document"
)

// buildJSONDoc builds a map for JSON.SET.
// Tags and Numerics are stored at the top level for FT.INDEX; __content and __vector are reserved.
func buildJSONDoc(doc *domdoc.Document) map[string]any {
	m := make(map[string]any)
	for k, v := range doc.Tags() {
		m[k] = v
	}
	for k, v := range doc.Numerics() {
		m[k] = v
	}
	m["__content"] = doc.Content()
	m["__vector"] = doc.Vector()
	return m
}

// parseJSONGetResult parses a JSON.GET result (array [{ ... }]).
func parseJSONGetResult(id, raw string) (domdoc.Document, error) {
	var docs []map[string]any
	if err := json.Unmarshal([]byte(raw), &docs); err != nil {
		return domdoc.Document{}, fmt.Errorf("unmarshal JSON.GET result: %w", err)
	}
	if len(docs) == 0 {
		return domdoc.Document{}, fmt.Errorf("empty JSON.GET result")
	}
	return parseDocMap(id, docs[0]), nil
}

// parseDocMap converts a raw map into a domain Document.
func parseDocMap(id string, m map[string]any) domdoc.Document {
	var content string
	tags := make(map[string]string)
	numerics := make(map[string]float64)
	var vector []float32

	for k, v := range m {
		switch k {
		case "__content":
			if s, ok := v.(string); ok {
				content = s
			}
		case "__vector":
			if arr, ok := v.([]any); ok {
				vector = make([]float32, len(arr))
				for i, el := range arr {
					if f, ok := el.(float64); ok {
						vector[i] = float32(f)
					}
				}
			}
		default:
			switch val := v.(type) {
			case string:
				tags[k] = val
			case float64:
				numerics[k] = val
			}
		}
	}

	return domdoc.Reconstruct(id, content, tags, numerics, vector, 0)
}
