package usage

// BudgetReader provides read-only access to token budget state.
type BudgetReader interface {
	DailyLimit() int64
	MonthlyLimit() int64
	DailyUsed() int64
	MonthlyUsed() int64
	RemainingDaily() int64
	RemainingMonthly() int64
}
