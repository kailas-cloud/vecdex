package document

import (
	"fmt"
	"regexp"
)

var (
	idRegex     = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)
	reservedIDs = map[string]bool{"search": true, "collections": true}
)

// MaxContentSize is the maximum document content size in bytes.
const MaxContentSize = 163840 // 160KB

// Document is the document aggregate (immutable value object).
type Document struct {
	id       string
	content  string
	tags     map[string]string
	numerics map[string]float64
	vector   []float32
	revision int
}

// New validates and creates a Document.
// ID: ^[a-zA-Z0-9_-]+$, 1-256 chars, not reserved.
// Content: non-empty, max 160KB. Tags/Numerics schema validation happens in the service layer.
func New(id, content string, tags map[string]string, numerics map[string]float64) (Document, error) {
	if id == "" {
		return Document{}, fmt.Errorf("document ID is required")
	}
	if len(id) > 256 {
		return Document{}, fmt.Errorf("document ID too long (max 256)")
	}
	if !idRegex.MatchString(id) {
		return Document{}, fmt.Errorf("document ID must be alphanumeric with underscores and hyphens")
	}
	if reservedIDs[id] {
		return Document{}, fmt.Errorf("document ID %q is reserved", id)
	}
	if content == "" {
		return Document{}, fmt.Errorf("content is required")
	}
	if len(content) > MaxContentSize {
		return Document{}, fmt.Errorf("content too large (max %d bytes)", MaxContentSize)
	}

	return Document{
		id:       id,
		content:  content,
		tags:     cloneStringMap(tags),
		numerics: cloneFloat64Map(numerics),
		revision: 1,
	}, nil
}

// Reconstruct creates a Document without validation (storage hydration).
func Reconstruct(
	id, content string, tags map[string]string, numerics map[string]float64,
	vector []float32, revision int,
) Document {
	return Document{id: id, content: content, tags: tags, numerics: numerics, vector: vector, revision: revision}
}

// ID returns the document identifier.
func (d *Document) ID() string { return d.id }

// Content returns the document text content.
func (d *Document) Content() string { return d.content }

// Tags returns the tag metadata fields.
func (d *Document) Tags() map[string]string { return d.tags }

// Numerics returns the numeric metadata fields.
func (d *Document) Numerics() map[string]float64 { return d.numerics }

// Vector returns the embedding vector.
func (d *Document) Vector() []float32 { return d.vector }

// Revision returns the document revision number.
func (d *Document) Revision() int { return d.revision }

// WithVector returns a copy with the given vector set.
func (d *Document) WithVector(v []float32) Document {
	return Document{
		id: d.id, content: d.content, tags: d.tags, numerics: d.numerics,
		vector: v, revision: d.revision,
	}
}

// SetVector sets the vector in place (mutation).
func (d *Document) SetVector(v []float32) { d.vector = v }

func cloneStringMap(m map[string]string) map[string]string {
	if m == nil {
		return nil
	}
	c := make(map[string]string, len(m))
	for k, v := range m {
		c[k] = v
	}
	return c
}

func cloneFloat64Map(m map[string]float64) map[string]float64 {
	if m == nil {
		return nil
	}
	c := make(map[string]float64, len(m))
	for k, v := range m {
		c[k] = v
	}
	return c
}
