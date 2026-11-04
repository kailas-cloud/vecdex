package db

// RecordMetadata preserves all response metadata in one projected hash field.
// Individual indexed fields remain available for filtering and ranking.
type RecordMetadata struct {
	Tags     map[string]string  `json:"tags"`
	Numerics map[string]float64 `json:"numerics"`
}
