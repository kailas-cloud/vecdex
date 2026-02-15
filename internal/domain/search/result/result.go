package result

// Result is a single search hit.
type Result struct {
	id       string
	score    float64
	content  string
	tags     map[string]string
	numerics map[string]float64
	vector   []float32
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

// Tags returns the document tags.
func (r *Result) Tags() map[string]string { return r.tags }

// Numerics returns the document numeric fields.
func (r *Result) Numerics() map[string]float64 { return r.numerics }

// Vector returns the document embedding vector.
func (r *Result) Vector() []float32 { return r.vector }
