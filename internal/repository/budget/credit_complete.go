package budget

import (
	"context"
	"fmt"
	"time"

	domusage "github.com/kailas-cloud/vecdex/internal/domain/usage"
)

// CompleteCredits accounts for a model reply on its original partition. Normal
// replies never read or write the shared allocation ledger.
func (s *Accounting) CompleteCredits(
	ctx context.Context, ticket *domusage.CreditTicket, tokens int64, unknown bool,
) error {
	if s.credits == nil {
		return fmt.Errorf("credit backend unavailable")
	}
	c := creditPoolCommand(ticket, "complete")
	c.Tokens, c.Unknown = tokens, unknown
	result, err := s.credits.CreditPool(ctx, c)
	if err != nil {
		return fmt.Errorf("complete local credit: %w", err)
	}
	if result.Closed {
		// The due entry repairs a failed refund after a committed completion.
		// A duplicate must also succeed after settled control metadata expires.
		if !result.Applied {
			return nil
		}
		return s.reconcileCreditGrant(ctx, ticket.Provider, ticket.GrantID, time.Now().UTC())
	}
	if result.Applied && result.Refund > 0 {
		s.restoreCreditHint(ticket, result.Refund)
	}
	return nil
}

func (s *Accounting) restoreCreditHint(ticket *domusage.CreditTicket, amount int64) {
	policy := creditPolicy{ticket.Provider, time.UnixMilli(ticket.At).UTC().Format("2006-01-02"),
		ticket.DailyLimit, ticket.MonthlyLimit}
	s.mu.Lock()
	cache := s.caches[policy]
	s.mu.Unlock()
	if cache == nil {
		return
	}
	for _, cached := range cache.liveGrants(time.Now().UnixMilli()) {
		if cached.grant.ID != ticket.GrantID {
			continue
		}
		for i := range cached.pools {
			pool := &cached.pools[i]
			if pool.partition == ticket.Partition && !pool.closed.Load() {
				pool.remaining.Add(amount)
			}
		}
	}
}

func (s *Accounting) forgetCreditGrant(provider, id string) {
	s.mu.Lock()
	caches := make([]*creditCache, 0, len(s.caches))
	for policy, cache := range s.caches {
		if policy.provider == provider {
			caches = append(caches, cache)
		}
	}
	s.mu.Unlock()
	for _, cache := range caches {
		for _, grant := range cache.liveGrants(time.Now().UnixMilli()) {
			if grant.grant.ID == id {
				for i := range grant.pools {
					grant.pools[i].closed.Store(true)
				}
			}
		}
	}
}
