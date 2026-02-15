package metrics

// Metrics holds embedding API usage for a time period.
type Metrics struct {
	embeddingRequests int
	tokens            int
	costMillidollars  int
}

// New creates a Metrics snapshot.
func New(requests, tokens, costMillidollars int) Metrics {
	return Metrics{embeddingRequests: requests, tokens: tokens, costMillidollars: costMillidollars}
}

// EmbeddingRequests returns the number of embedding API calls.
func (m Metrics) EmbeddingRequests() int { return m.embeddingRequests }

// Tokens returns the total tokens consumed.
func (m Metrics) Tokens() int { return m.tokens }

// CostMillidollars returns cost in millicents (1 USD = 1000).
func (m Metrics) CostMillidollars() int { return m.costMillidollars }
