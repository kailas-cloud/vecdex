package result

import "github.com/kailas-cloud/vecdex/internal/domain/metadata"

// Result is a single search hit.
type Result struct {
	source   *sourceProjection
	id       string
	score    float64
	content  string
	tags     map[string]string
	numerics map[string]float64
	vector   []float32
}

type sourceProjection struct {
	version          string
	metadata         metadata.Object
	contentTruncated bool
}

// New creates a search result.
func New(
	id string, score float64, content string,
	tags map[string]string, numerics map[string]float64,
	vector []float32,
) Result {
	return Result{
		id: id, score: score, content: content,
		tags: tags, numerics: numerics, vector: vector,
	}
}

// ID returns the document identifier.
func (r *Result) ID() string { return r.id }

// Score returns the relevance score.
func (r *Result) Score() float64 { return r.score }

// Content returns the document content.
func (r *Result) Content() string { return r.content }

// ContentTruncated reports whether the candidate excerpt was shortened by a
// byte budget. False does not imply that a private fragment is the full source.
func (r *Result) ContentTruncated() bool { return r.source != nil && r.source.contentTruncated }

// Tags returns the document tags.
func (r *Result) Tags() map[string]string { return r.tags }

// Numerics returns the document numeric fields.
func (r *Result) Numerics() map[string]float64 { return r.numerics }

// Vector returns the document embedding vector.
func (r *Result) Vector() []float32 { return r.vector }

// Metadata returns the complete immutable source metadata.
func (r *Result) Metadata() metadata.Object {
	if r.source == nil {
		return metadata.Object{}
	}
	return r.source.metadata
}

// Version is the opaque token required for conditional source updates.
func (r *Result) Version() string {
	if r.source == nil {
		return ""
	}
	return r.source.version
}

// WithMetadata attaches source metadata while retaining its version.
func (r Result) WithMetadata(value metadata.Object) Result { return r.WithSource(r.Version(), value) }

// WithSource attaches one consistent source version and metadata projection.
func (r Result) WithSource(version string, value metadata.Object) Result {
	r.source = &sourceProjection{version: version, metadata: value, contentTruncated: r.ContentTruncated()}
	return r
}

// WithSourceOf retains a source projection when ranking changes the snippet.
func (r Result) WithSourceOf(from *Result) Result {
	truncated := r.ContentTruncated()
	r.source = from.source
	if truncated {
		r = r.withContentTruncated()
	}
	return r
}

// WithSnippet replaces only the returned excerpt, preserving source identity.
func (r Result) WithSnippet(text string) Result {
	if len(text) < len(r.content) {
		r = r.withContentTruncated()
	}
	r.content = text
	return r
}

func (r Result) withContentTruncated() Result {
	if r.ContentTruncated() {
		return r
	}
	source := sourceProjection{}
	if r.source != nil {
		source = *r.source
	}
	source.contentTruncated = true
	r.source = &source
	return r
}
