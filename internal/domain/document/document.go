package document

import (
	"fmt"
	"regexp"
	"unicode/utf8"

	"github.com/kailas-cloud/vecdex/internal/domain"
	"github.com/kailas-cloud/vecdex/internal/domain/metadata"
)

var (
	idRegex     = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)
	reservedIDs = map[string]bool{"search": true, "collections": true}
)

// MaxContentSize is the maximum document content size in bytes.
const MaxContentSize = 163840 // 160KB

// Document is the document aggregate (immutable value object).
type Document struct {
	metadata  metadata.Object
	id        string
	content   string
	tags      map[string]string
	numerics  map[string]float64
	vector    []float32
	version   string
	expiresAt int64
	revision  int
	fragments *[]Fragment
}

// New validates and creates a Document.
// ID: ^[a-zA-Z0-9_-]+$, 1-256 chars, not reserved.
// Content: max 160KB.
// Tags/Numerics schema validation happens in the service layer.
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
	if !utf8.ValidString(content) {
		return Document{}, fmt.Errorf("content must be valid UTF-8: %w", domain.ErrInvalidSchema)
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
	updated := *d
	updated.vector = v
	return updated
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

// SetRevision sets the revision returned by an atomic storage commit.
func (d *Document) SetRevision(revision int) { d.revision = revision }

// Fragment is one exact source span with its document embedding.
type Fragment struct {
	Content string
	Vector  []float32
}

// Fragments returns prepared search spans; a normal document contains one.
func (d *Document) Fragments() []Fragment {
	if d.fragments == nil {
		return nil
	}
	return *d.fragments
}

// SetFragments attaches a complete preparation result before atomic replacement.
func (d *Document) SetFragments(parts []Fragment) { d.fragments = &parts }

// Version identifies this exact committed source incarnation and revision.
func (d *Document) Version() string { return d.version }

// SetVersion attaches the opaque version returned by storage.
func (d *Document) SetVersion(version string) { d.version = version }
