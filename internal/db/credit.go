package db

import (
	"context"
	"errors"
)

var (
	// ErrCreditPoolClosed rejects spending from an expired or recovered lease.
	ErrCreditPoolClosed = errors.New("credit pool is closed")
	// ErrCreditPoolEmpty asks the allocator for another pool.
	ErrCreditPoolEmpty = errors.New("credit pool has insufficient capacity")
	// ErrCreditPolicyChanged prevents mixing limits within an allocation period.
	ErrCreditPolicyChanged = errors.New("credit policy differs from the active period")
)

// CreditSlice places one bounded part of a grant in a usage partition.
type CreditSlice struct {
	Partition int   `json:"partition"`
	Amount    int64 `json:"amount"`
}

// CreditGrant is immutable once allocated. The control slot reserves Amount
// before any local pool is installed or allowed to admit upstream work.
type CreditGrant struct {
	ID        string        `json:"id"`
	At        int64         `json:"at"`
	ExpiresAt int64         `json:"expires_at"`
	Amount    int64         `json:"amount"`
	Slices    []CreditSlice `json:"slices"`
}

// CreditGrantCommand changes a provider's day/month allocation ledger. All
// control keys share one slot; no invocation updates this ledger directly.
type CreditGrantCommand struct {
	Action, DayKey, MonthKey, GrantKey, ActiveKey, DueKey string
	Grant                                                 CreditGrant
	Partitions                                            []int
	Wanted, Minimum, DailyLimit, MonthlyLimit             int64
	BaselineDay, BaselineMonth, UnknownDay, UnknownMonth  int64
	// Refund is cumulative and accepted only after every slice has been fenced.
	Refund, NextCheck int64
	KeepDue           bool
}

// CreditGrantResult includes the durable cumulative refund high-water mark.
type CreditGrantResult struct {
	Grant    CreditGrant
	Released int64
	Closed   bool
	Active   bool
}

// CreditPoolCommand is one same-slot transition. CounterKeys contains the fixed
// day/month/total ledger of the grant's original UTC periods. Receipts retain
// the original reservation so completion and restart recovery are idempotent.
type CreditPoolCommand struct {
	Action, GrantID, PoolKey, LiveKey, ReceiptKey string
	CounterKeys                                   []string
	Capacity, ExpiresAt, Amount, Tokens           int64
	Unknown                                       bool
}

// CreditPoolResult reports the balance after a validated same-slot transition.
type CreditPoolResult struct {
	Capacity, Available, Pending, PendingAmount int64
	Closed                                      bool
	Applied                                     bool
	Refund                                      int64
}

// CreditStore allocates shared credits and spends them on independent slots.
type CreditStore interface {
	CreditGrant(ctx context.Context, command *CreditGrantCommand) (CreditGrantResult, error)
	CreditPool(ctx context.Context, command *CreditPoolCommand) (CreditPoolResult, error)
}
