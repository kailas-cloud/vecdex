package valkey

import (
	"fmt"
	"math"

	"github.com/kailas-cloud/vecdex/internal/db"
	domusage "github.com/kailas-cloud/vecdex/internal/domain/usage"
)

func applyCreditPool(c *db.CreditPoolCommand, t *creditPoolTransition, live bool, now int64) error {
	if c.Action == "install" || c.Action == "close" {
		return installOrCloseCreditPool(c, t, now)
	}
	if t.pool.GrantID == "" {
		return fmt.Errorf("credit pool is missing")
	}
	if err := validateCreditCounters(&t.counters, &t.pool); err != nil {
		return err
	}
	if err := validateCreditInvocation(&t.receipt, &t.pool); err != nil {
		return err
	}
	if c.Action == "reserve" {
		return reserveCreditPool(c, t, live, now)
	}
	if t.receipt.GrantID == "" {
		return fmt.Errorf("credit invocation is missing")
	}
	if t.receipt.Done {
		return nil
	}
	if !live || t.pool.ExpiresAt <= now {
		if err := closeCreditPool(t); err != nil {
			return err
		}
	}
	if c.Action == "cancel" {
		return cancelCreditPool(t)
	}
	return completeCreditPool(c, t)
}

func cancelCreditPool(t *creditPoolTransition) error {
	if err := updateCreditCounters(&t.counters, func(counter *domusage.Counters) error {
		if err := changeCreditCounter(&counter.Reserved, -t.receipt.Amount); err != nil {
			return err
		}
		if !t.pool.Closed {
			return nil
		}
		if err := changeCreditCounter(&counter.Requests, -1); err != nil {
			return err
		}
		return changeCreditCounter(&counter.Unknown, -1)
	}); err != nil {
		return err
	}
	t.refund = t.receipt.Amount
	t.pool.Available += t.refund
	t.pool.Pending--
	t.pool.PendingAmount -= t.receipt.Amount
	t.receipt.Done = true
	t.writeCounters, t.writeReceipt, t.changed = true, true, true
	return nil
}

func installOrCloseCreditPool(c *db.CreditPoolCommand, t *creditPoolTransition, now int64) error {
	if t.pool.GrantID == "" {
		t.pool = creditPoolState{GrantID: c.GrantID, Capacity: c.Capacity,
			Available: c.Capacity, ExpiresAt: c.ExpiresAt}
		t.changed = true
	} else if t.pool.Capacity != c.Capacity || t.pool.ExpiresAt != c.ExpiresAt {
		return fmt.Errorf("credit pool allocation differs from grant")
	}
	if c.Action == "close" {
		if err := validateCreditCounters(&t.counters, &t.pool); err != nil {
			return err
		}
		return closeCreditPool(t)
	}
	if t.pool.Closed || c.ExpiresAt <= now {
		return db.ErrCreditPoolClosed
	}
	return nil
}

func validateCreditCounters(snapshot *domusage.Snapshot, pool *creditPoolState) error {
	for _, counter := range []*domusage.Counters{&snapshot.Day, &snapshot.Month, &snapshot.Total} {
		if counter.Reserved < pool.PendingAmount || counter.Unknown > counter.Requests ||
			(pool.Closed && counter.Unknown < pool.Pending) {
			return fmt.Errorf("credit pool usage reservations are missing")
		}
	}
	return nil
}

func validateCreditInvocation(receipt *creditInvocation, pool *creditPoolState) error {
	if receipt.GrantID == "" {
		if receipt.Amount != 0 || receipt.Done {
			return fmt.Errorf("invalid stored credit invocation")
		}
		return nil
	}
	if receipt.GrantID != pool.GrantID || receipt.Amount <= 0 || receipt.Amount > pool.Capacity {
		return fmt.Errorf("credit invocation differs from pool")
	}
	if !receipt.Done && (pool.Pending == 0 || pool.PendingAmount < receipt.Amount) {
		return fmt.Errorf("credit invocation reservation is missing")
	}
	return nil
}

func reserveCreditPool(c *db.CreditPoolCommand, t *creditPoolTransition, live bool, now int64) error {
	if t.pool.Closed || !live || t.pool.ExpiresAt <= now {
		return db.ErrCreditPoolClosed
	}
	if t.receipt.GrantID != "" {
		if t.receipt.Done || t.receipt.Amount != c.Amount {
			return fmt.Errorf("credit invocation cannot authorize another spend")
		}
		return nil
	}
	if t.pool.Available < c.Amount {
		return db.ErrCreditPoolEmpty
	}
	if err := updateCreditCounters(&t.counters, func(counter *domusage.Counters) error {
		return changeCreditCounter(&counter.Reserved, c.Amount)
	}); err != nil {
		return err
	}
	t.pool.Available -= c.Amount
	t.pool.Pending++ // Each pending invocation owns at least one unit of capacity.
	t.pool.PendingAmount += c.Amount
	t.receipt = creditInvocation{GrantID: c.GrantID, Amount: c.Amount}
	t.writeCounters, t.writeReceipt, t.changed = true, true, true
	return nil
}

func closeCreditPool(t *creditPoolTransition) error {
	if t.pool.Closed {
		return nil
	}
	if t.pool.Pending > 0 {
		if err := updateCreditCounters(&t.counters, func(counter *domusage.Counters) error {
			if counter.Reserved < t.pool.PendingAmount {
				return fmt.Errorf("credit pool reservations are missing")
			}
			if err := changeCreditCounter(&counter.Requests, t.pool.Pending); err != nil {
				return err
			}
			return changeCreditCounter(&counter.Unknown, t.pool.Pending)
		}); err != nil {
			return err
		}
		t.writeCounters = true
	}
	t.pool.Closed, t.changed = true, true
	return nil
}

func completeCreditPool(c *db.CreditPoolCommand, t *creditPoolTransition) error {
	if c.Tokens > t.receipt.Amount {
		return fmt.Errorf("credit usage exceeds reserved upper bound")
	}
	if err := updateCreditCounters(&t.counters, func(counter *domusage.Counters) error {
		return completeCreditCounter(counter, c, &t.receipt, t.pool.Closed)
	}); err != nil {
		return err
	}
	if !c.Unknown {
		t.refund = t.receipt.Amount - c.Tokens
		t.pool.Available += t.refund
	}
	t.pool.Pending--
	t.pool.PendingAmount -= t.receipt.Amount
	t.receipt.Done = true
	t.writeCounters, t.writeReceipt, t.changed = true, true, true
	return nil
}

func completeCreditCounter(
	counter *domusage.Counters, c *db.CreditPoolCommand, receipt *creditInvocation, closed bool,
) error {
	if counter.Reserved < receipt.Amount {
		return fmt.Errorf("credit invocation reservation is missing")
	}
	if err := changeCreditCounter(&counter.Tokens, c.Tokens); err != nil {
		return err
	}
	requests, unknown := int64(1), int64(0)
	if c.Unknown {
		unknown = 1
	}
	if closed {
		requests, unknown = 0, unknown-1
	}
	if err := changeCreditCounter(&counter.Requests, requests); err != nil {
		return err
	}
	if err := changeCreditCounter(&counter.Unknown, unknown); err != nil {
		return err
	}
	if !c.Unknown {
		counter.Reserved -= receipt.Amount
	} else {
		// The upper bound is already booked globally. Move the known lower bound
		// from reserved to measured usage without releasing any credit.
		counter.Reserved -= c.Tokens
	}
	return nil
}

func updateCreditCounters(snapshot *domusage.Snapshot, update func(*domusage.Counters) error) error {
	for _, counter := range []*domusage.Counters{&snapshot.Day, &snapshot.Month, &snapshot.Total} {
		if err := update(counter); err != nil {
			return err
		}
	}
	return nil
}

func changeCreditCounter(counter *int64, delta int64) error {
	if *counter < 0 || (delta > 0 && *counter > math.MaxInt64-delta) || (delta < 0 && delta < -*counter) {
		return fmt.Errorf("invalid credit counter transition")
	}
	*counter += delta
	return nil
}
