package valkey

import (
	"fmt"
	"math"

	"github.com/kailas-cloud/vecdex/internal/db"
	"github.com/kailas-cloud/vecdex/internal/domain"
)

func applyCreditGrant(c *db.CreditGrantCommand, state *creditGrantTransition, now int64) error {
	if state.found && (!state.day.present || !state.month.present) {
		return fmt.Errorf("credit grant allocation period is missing")
	}
	if c.Action == "refund" {
		return refundCreditGrant(c, state)
	}
	if c.Action == "activate" {
		return activateCreditGrant(state, now)
	}
	if (state.day.present && state.day.Limit != c.DailyLimit) ||
		(state.month.present && state.month.Limit != c.MonthlyLimit) {
		return db.ErrCreditPolicyChanged
	}
	if c.Action == "initialize" {
		return initializeCreditGrant(c, state)
	}
	if state.found {
		if state.result.Grant.At != c.Grant.At || state.result.Grant.ExpiresAt != c.Grant.ExpiresAt {
			return fmt.Errorf("credit grant identity was reused")
		}
		return nil
	}
	if c.Grant.ExpiresAt <= now {
		return db.ErrCreditPoolClosed
	}
	return allocateCreditGrant(c, state)
}

func activateCreditGrant(state *creditGrantTransition, now int64) error {
	if !state.found {
		return fmt.Errorf("cannot activate a missing credit grant")
	}
	if state.result.Closed || state.result.Grant.ExpiresAt <= now {
		return db.ErrCreditPoolClosed
	}
	state.changed = !state.result.Active
	state.result.Active = true
	return nil
}

func initializeCreditGrant(c *db.CreditGrantCommand, state *creditGrantTransition) error {
	if err := prepareCreditAllocation(&state.day, c.DailyLimit, c.BaselineDay, c.UnknownDay); err != nil {
		return err
	}
	if err := prepareCreditAllocation(&state.month, c.MonthlyLimit, c.BaselineMonth, c.UnknownMonth); err != nil {
		return err
	}
	state.changed = true
	return nil
}

func allocateCreditGrant(c *db.CreditGrantCommand, state *creditGrantTransition) error {
	if err := prepareCreditAllocation(&state.day, c.DailyLimit, c.BaselineDay, c.UnknownDay); err != nil {
		return err
	}
	if err := prepareCreditAllocation(&state.month, c.MonthlyLimit, c.BaselineMonth, c.UnknownMonth); err != nil {
		return err
	}
	amount := min(c.Wanted, remainingCreditAllocation(&state.day), remainingCreditAllocation(&state.month))
	if amount < c.Minimum {
		return domain.ErrEmbeddingQuotaExceeded
	}
	if err := changeCreditCounter(&state.day.Booked, amount); err != nil {
		return err
	}
	if err := changeCreditCounter(&state.month.Booked, amount); err != nil {
		return err
	}
	count := min(int64(len(c.Partitions)), amount/c.Minimum)
	grant := db.CreditGrant{ID: c.Grant.ID, At: c.Grant.At, ExpiresAt: c.Grant.ExpiresAt, Amount: amount,
		Slices: make([]db.CreditSlice, count)}
	for i := range grant.Slices {
		grant.Slices[i] = db.CreditSlice{Partition: c.Partitions[i], Amount: amount / count}
	}
	grant.Slices[len(grant.Slices)-1].Amount += amount % count
	state.result = db.CreditGrantResult{Grant: grant}
	state.found, state.changed = true, true
	return nil
}

func prepareCreditAllocation(period *creditAllocationPeriod, limit, baseline, unknown int64) error {
	if period.present {
		if period.Limit != limit {
			return db.ErrCreditPolicyChanged
		}
		return nil
	}
	if unknown > math.MaxInt64-baseline {
		return fmt.Errorf("credit allocation baseline overflow")
	}
	*period = creditAllocationPeriod{Booked: baseline + unknown, Limit: limit,
		Baseline: baseline, UnknownBaseline: unknown, present: true}
	return nil
}

func remainingCreditAllocation(period *creditAllocationPeriod) int64 {
	if period.Limit == 0 {
		return math.MaxInt64 - period.Booked
	}
	return max(int64(0), period.Limit-period.Booked)
}

func refundCreditGrant(c *db.CreditGrantCommand, state *creditGrantTransition) error {
	if !state.found {
		return fmt.Errorf("cannot refund a missing credit grant")
	}
	if c.Refund > state.result.Grant.Amount {
		return fmt.Errorf("credit refund exceeds grant capacity")
	}
	refund := max(int64(0), c.Refund-state.result.Released)
	for _, period := range []*creditAllocationPeriod{&state.day, &state.month} {
		baseline := period.Baseline + period.UnknownBaseline
		if period.Booked-baseline < refund {
			return fmt.Errorf("credit refund exceeds booked allocation")
		}
		period.Booked -= refund
	}
	state.result.Released += refund
	state.result.Closed, state.changed = true, true
	state.result.Active = false
	return nil
}
