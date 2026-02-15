package patch

import "fmt"

// MaxContentSize is the maximum allowed content size in bytes.
const MaxContentSize = 163840 // 160KB

// Patch is a partial document update.
// Nil fields are unchanged. A nil value in Tags/Numerics means delete that field.
type Patch struct {
	content  *string
	tags     map[string]*string
	numerics map[string]*float64
}

// New validates and creates a Patch. At least one field must be provided.
func New(content *string, tags map[string]*string, numerics map[string]*float64) (Patch, error) {
	if content == nil && len(tags) == 0 && len(numerics) == 0 {
		return Patch{}, fmt.Errorf("at least one field must be provided")
	}
	if content != nil && len(*content) > MaxContentSize {
		return Patch{}, fmt.Errorf("content too large (max %d bytes)", MaxContentSize)
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
