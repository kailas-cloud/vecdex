package search

import "math"

// Config controls retrieval depth before document aggregation.
type Config struct {
	SemanticCandidateFloor      int
	SemanticCandidateMultiplier float64
	BM25CandidateFloor          int
	BM25CandidateMultiplier     float64
}

// DefaultConfig returns search defaults tuned for chunk retrieval.
func DefaultConfig() Config {
	return Config{
		SemanticCandidateFloor:      100,
		SemanticCandidateMultiplier: 2.0,
		BM25CandidateFloor:          100,
		BM25CandidateMultiplier:     2.0,
	}
}

func normalizeConfig(cfg Config) Config {
	defaults := DefaultConfig()
	if cfg.SemanticCandidateFloor <= 0 {
		cfg.SemanticCandidateFloor = defaults.SemanticCandidateFloor
	}
	if cfg.SemanticCandidateMultiplier <= 0 {
		cfg.SemanticCandidateMultiplier = defaults.SemanticCandidateMultiplier
	}
	if cfg.BM25CandidateFloor <= 0 {
		cfg.BM25CandidateFloor = defaults.BM25CandidateFloor
	}
	if cfg.BM25CandidateMultiplier <= 0 {
		cfg.BM25CandidateMultiplier = defaults.BM25CandidateMultiplier
	}
	return cfg
}

func candidateWindow(docWindow, limit, floor int, multiplier float64) int {
	window := max(docWindow+1, limit+1, floor)
	scaled := int(math.Ceil(float64(docWindow) * multiplier))
	return max(window, scaled)
}
