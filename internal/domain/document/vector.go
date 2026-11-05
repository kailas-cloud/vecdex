package document

import (
	"math"
	"unicode/utf8"
)

// RepresentativeVector computes the length-weighted, normalized source vector.
// A single fragment retains its exact embedding bits.
func RepresentativeVector(parts []Fragment) []float32 {
	if len(parts) == 0 {
		return nil
	}
	if len(parts) == 1 {
		return parts[0].Vector
	}
	sum := make([]float64, len(parts[0].Vector))
	for _, part := range parts {
		weight := float64(utf8.RuneCountInString(part.Content))
		for i, value := range part.Vector {
			sum[i] += float64(value) * weight
		}
	}
	var norm float64
	for _, value := range sum {
		norm += value * value
	}
	norm = math.Sqrt(norm)
	if norm == 0 {
		return parts[0].Vector
	}
	vector := make([]float32, len(sum))
	for i, value := range sum {
		vector[i] = float32(value / norm)
	}
	return vector
}
