package mode

// Mode is the search strategy.
type Mode string

// Search mode constants.
const (
	// Hybrid combines semantic and keyword search.
	Hybrid   Mode = "hybrid"
	Semantic Mode = "semantic"
	Keyword  Mode = "keyword"
)

// IsValid checks if the mode is one of the supported values.
func (m Mode) IsValid() bool {
	return m == Hybrid || m == Semantic || m == Keyword
}
