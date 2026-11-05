package budget

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/kailas-cloud/vecdex/internal/db"
	"github.com/kailas-cloud/vecdex/internal/domain"
	domusage "github.com/kailas-cloud/vecdex/internal/domain/usage"
)

// ReserveCredits admits an invocation from independent local pools. A shared
// grant is acquired only when cached portions cannot fit the invocation bound.
func (s *Accounting) ReserveCredits(
	ctx context.Context, provider, id string, now time.Time, amount, daily, monthly int64,
) (domusage.CreditTicket, error) {
	if s.credits == nil {
		return domusage.CreditTicket{}, fmt.Errorf("credit backend unavailable")
	}
	if provider == "" || id == "" || amount <= 0 || daily < 0 || monthly < 0 || (daily == 0 && monthly == 0) {
		return domusage.CreditTicket{}, fmt.Errorf("invalid credit reservation")
	}
	cache := s.creditCacheFor(provider, now, daily, monthly)
	ticket, found, err := s.tryCachedCredits(ctx, cache, provider, id, now, amount, daily, monthly)
	if err != nil || found {
		return ticket, err
	}
	select {
	case cache.gate <- struct{}{}:
		defer func() { <-cache.gate }()
	case <-ctx.Done():
		return ticket, fmt.Errorf("wait for credit allocation: %w", ctx.Err())
	}
	return s.reserveCreditsSlow(ctx, cache, provider, id, now, amount, daily, monthly)
}

func (s *Accounting) reserveCreditsSlow(
	ctx context.Context, cache *creditCache, provider, id string, now time.Time, amount, daily, monthly int64,
) (domusage.CreditTicket, error) {
	ticket, found, err := s.tryCachedCredits(ctx, cache, provider, id, now, amount, daily, monthly)
	if err != nil || found {
		return ticket, err
	}
	if !cache.initialized {
		if err := s.initializeCreditHistory(ctx, provider, now, daily, monthly); err != nil {
			return ticket, err
		}
		cache.initialized = true
	}
	ticket, err = s.allocateCreditInvocation(ctx, cache, provider, id, now, amount, daily, monthly)
	if !errors.Is(err, domain.ErrEmbeddingQuotaExceeded) {
		return ticket, err
	}
	if err := s.reclaimActiveCredits(ctx, provider, now); err != nil {
		return ticket, err
	}
	return s.allocateCreditInvocation(ctx, cache, provider, id, now, amount, daily, monthly)
}

func (s *Accounting) tryCachedCredits(
	ctx context.Context, cache *creditCache, provider, id string, now time.Time, amount, daily, monthly int64,
) (domusage.CreditTicket, bool, error) {
	for _, cached := range cache.liveGrants(now.UnixMilli()) {
		start := int(cache.next.Add(1)&7) % len(cached.pools)
		for offset := range cached.pools {
			pool := &cached.pools[(start+offset)%len(cached.pools)]
			if !claimCreditHint(pool, amount) {
				continue
			}
			ticket := domusage.CreditTicket{Provider: provider, ID: id, GrantID: cached.grant.ID, Partition: pool.partition,
				At: cached.grant.At, ExpiresAt: cached.grant.ExpiresAt, Amount: amount, DailyLimit: daily, MonthlyLimit: monthly}
			_, err := s.credits.CreditPool(ctx, creditPoolCommand(&ticket, "reserve"))
			switch {
			case err == nil:
				return ticket, true, nil
			case errors.Is(err, db.ErrCreditPoolClosed):
				pool.closed.Store(true)
			case errors.Is(err, db.ErrCreditPoolEmpty):
				pool.remaining.Store(0)
			default:
				return domusage.CreditTicket{}, false, fmt.Errorf("reserve local credit: %w", err)
			}
		}
	}
	return domusage.CreditTicket{}, false, nil
}

func (s *Accounting) allocateCreditInvocation(
	ctx context.Context, cache *creditCache, provider, requestID string, now time.Time, amount, daily, monthly int64,
) (domusage.CreditTicket, error) {
	id := rand.Text()
	c := creditControl(provider, id, now)
	c.Action, c.Minimum, c.Wanted, c.DailyLimit, c.MonthlyLimit = "grant", amount, math.MaxInt64, daily, monthly
	if amount <= math.MaxInt64/32 {
		c.Wanted = amount * 32
	}
	dayEnd := time.Date(now.UTC().Year(), now.UTC().Month(), now.UTC().Day()+1, 0, 0, 0, 0, time.UTC)
	c.Grant.ExpiresAt = min(now.Add(creditLeaseLifetime).UnixMilli(), dayEnd.UnixMilli())
	start := usagePartition(id)
	for i := range 8 {
		c.Partitions = append(c.Partitions, (start+i*31)%UsagePartitionCount)
	}
	result, err := s.credits.CreditGrant(ctx, c)
	if err != nil {
		return domusage.CreditTicket{}, fmt.Errorf("allocate shared credit: %w", err)
	}
	if result.Closed {
		return domusage.CreditTicket{}, fmt.Errorf("allocated grant is already closed")
	}
	for _, slice := range result.Grant.Slices {
		ticket := domusage.CreditTicket{Provider: provider, GrantID: id, At: result.Grant.At,
			ExpiresAt: result.Grant.ExpiresAt, Partition: slice.Partition}
		pool := creditPoolCommand(&ticket, "install")
		pool.Capacity, pool.ReceiptKey = slice.Amount, ""
		if _, err := s.credits.CreditPool(ctx, pool); err != nil {
			return domusage.CreditTicket{}, fmt.Errorf("install allocated credit: %w", err)
		}
	}
	ticket := domusage.CreditTicket{Provider: provider, ID: requestID, GrantID: id, At: result.Grant.At,
		ExpiresAt: result.Grant.ExpiresAt, Partition: result.Grant.Slices[0].Partition,
		Amount: amount, DailyLimit: daily, MonthlyLimit: monthly}
	if _, err := s.credits.CreditPool(ctx, creditPoolCommand(&ticket, "reserve")); err != nil {
		return domusage.CreditTicket{}, fmt.Errorf("reserve first grant invocation: %w", err)
	}
	c.Action = "activate"
	if _, err := s.credits.CreditGrant(ctx, c); err != nil {
		// No upstream call has started. Reconcile the definitely-held reservation
		// as zero cost; the durable due entry will reclaim all other pool balances.
		cleanup, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		complete := creditPoolCommand(&ticket, "cancel")
		_, cleanupErr := s.credits.CreditPool(cleanup, complete)
		return domusage.CreditTicket{}, errors.Join(fmt.Errorf("activate allocated credit: %w", err), cleanupErr)
	}
	cache.publish(result.Grant, amount)
	return ticket, nil
}
