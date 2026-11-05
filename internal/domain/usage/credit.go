package usage

// CreditTicket captures the original periods and local reservation. It is
// internal accounting state, never a client-visible authorization token.
type CreditTicket struct {
	Provider, ID, GrantID    string
	Partition                int
	At, ExpiresAt, Amount    int64
	DailyLimit, MonthlyLimit int64
}
