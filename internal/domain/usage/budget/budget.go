package budget

// Budget tracks embedding API token budget state.
type Budget struct {
	tokensLimit     int
	tokensRemaining int
	isExhausted     bool
	resetsAt        int64 // unix millis, converted to ISO 8601 at transport layer
}

// New creates a Budget snapshot.
func New(limit, remaining int, isExhausted bool, resetsAt int64) Budget {
	return Budget{
		tokensLimit:     limit,
		tokensRemaining: remaining,
		isExhausted:     isExhausted,
		resetsAt:        resetsAt,
	}
}

// TokensLimit returns the token cap.
func (b Budget) TokensLimit() int { return b.tokensLimit }

// TokensRemaining returns tokens left.
func (b Budget) TokensRemaining() int { return b.tokensRemaining }

// IsExhausted reports whether the budget is spent.
func (b Budget) IsExhausted() bool { return b.isExhausted }

// ResetsAt returns the reset timestamp (unix millis).
func (b Budget) ResetsAt() int64 { return b.resetsAt }
