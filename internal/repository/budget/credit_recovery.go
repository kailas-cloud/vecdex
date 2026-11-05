package budget

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	domusage "github.com/kailas-cloud/vecdex/internal/domain/usage"
)

// ReconcileCredits fences due grants and refunds only their proven unused
// balance. Durable manifests make the same operation safe after process loss.
func (s *Accounting) ReconcileCredits(ctx context.Context, provider string, now time.Time, limit int) error {
	if s.credits == nil {
		return fmt.Errorf("credit backend unavailable")
	}
	if limit <= 0 {
		return fmt.Errorf("credit recovery limit must be positive")
	}
	control := creditControl(provider, "", now)
	ids, err := s.credits.ExpiryIDs(ctx, control.DueKey, now.UnixMilli(), min(limit, creditRecoveryBatch))
	if err != nil {
		return fmt.Errorf("read due credit grants: %w", err)
	}
	return s.reconcileCreditGrants(ctx, provider, ids, now)
}

func (s *Accounting) reclaimActiveCredits(ctx context.Context, provider string, now time.Time) error {
	if err := s.ReconcileCredits(ctx, provider, now, creditRecoveryBatch); err != nil {
		return err
	}
	control := creditControl(provider, "", now)
	ids, err := s.credits.CatalogIDs(ctx, control.ActiveKey, "", creditRecoveryBatch)
	if err != nil {
		return fmt.Errorf("read active credit grants: %w", err)
	}
	return s.reconcileCreditGrants(ctx, provider, ids, now)
}

func (s *Accounting) reconcileCreditGrants(ctx context.Context, provider string, ids []string, now time.Time) error {
	jobs := make(chan int, len(ids))
	for i := range ids {
		jobs <- i
	}
	close(jobs)
	failures := make([]error, len(ids))
	var workers sync.WaitGroup
	for range min(4, len(ids)) {
		workers.Go(func() {
			for i := range jobs {
				failures[i] = s.reconcileCreditGrant(ctx, provider, ids[i], now)
			}
		})
	}
	workers.Wait()
	return errors.Join(failures...)
}

func (s *Accounting) reconcileCreditGrant(ctx context.Context, provider, id string, now time.Time) error {
	read := creditControl(provider, id, now)
	read.Action = "read"
	manifest, err := s.credits.CreditGrant(ctx, read)
	if err != nil {
		return fmt.Errorf("read credit recovery manifest: %w", err)
	}
	s.forgetCreditGrant(provider, id)
	at := time.UnixMilli(manifest.Grant.At).UTC()
	var unused, pending int64
	for _, slice := range manifest.Grant.Slices {
		ticket := domusage.CreditTicket{Provider: provider, GrantID: id, At: manifest.Grant.At,
			ExpiresAt: manifest.Grant.ExpiresAt, Partition: slice.Partition}
		c := creditPoolCommand(&ticket, "close")
		c.Capacity, c.ReceiptKey = slice.Amount, ""
		result, err := s.credits.CreditPool(ctx, c)
		if err != nil {
			return fmt.Errorf("fence credit pool for recovery: %w", err)
		}
		if !result.Closed || result.Available > manifest.Grant.Amount-unused ||
			result.Pending > manifest.Grant.Amount-pending {
			return fmt.Errorf("invalid closed credit balance")
		}
		unused, pending = unused+result.Available, pending+result.Pending
	}
	c := creditControl(provider, id, at)
	c.Action, c.Refund = "refund", unused
	monthEnd := time.Date(at.Year(), at.Month()+1, 1, 0, 0, 0, 0, time.UTC)
	c.KeepDue, c.NextCheck = pending > 0 && now.Before(monthEnd), now.Add(5*time.Second).UnixMilli()
	if _, err := s.credits.CreditGrant(ctx, c); err != nil {
		return fmt.Errorf("return unused shared credit: %w", err)
	}
	return nil
}
