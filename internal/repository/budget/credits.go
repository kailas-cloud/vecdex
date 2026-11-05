package budget

import (
	"context"
	"crypto/sha256"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kailas-cloud/vecdex/internal/db"
	domusage "github.com/kailas-cloud/vecdex/internal/domain/usage"
)

const creditLeaseLifetime = 30 * time.Second
const creditCacheGrantLimit = 16
const creditRecoveryBatch = 32

type creditBackend interface {
	db.CreditStore
	CatalogIDs(context.Context, string, string, int) ([]string, error)
	ExpiryIDs(context.Context, string, int64, int) ([]string, error)
	HGetAllMulti(context.Context, []string) ([]map[string]string, error)
}

type creditPolicy struct {
	provider, day  string
	daily, monthly int64
}

type creditCache struct {
	mu          sync.Mutex
	gate        chan struct{}
	initialized bool // Accessed only while holding gate.
	grants      []*cachedCreditGrant
	next        atomic.Uint64
}

type cachedCreditGrant struct {
	grant db.CreditGrant
	pools []cachedCreditPool
}

type cachedCreditPool struct {
	partition int
	remaining atomic.Int64
	closed    atomic.Bool
}

func creditControl(provider, id string, at time.Time) *db.CreditGrantCommand {
	base := fmt.Sprintf("vecdex:v3:credits:{%x}", sha256.Sum256([]byte(provider)))
	return &db.CreditGrantCommand{GrantKey: base + ":grant:" + id,
		DayKey: base + ":day:" + at.UTC().Format("2006-01-02"), MonthKey: base + ":month:" + at.UTC().Format("2006-01"),
		ActiveKey: base + ":active", DueKey: base + ":due", Grant: db.CreditGrant{ID: id, At: at.UnixMilli()}}
}

func creditUsageKeys(provider, grant, id string, partition int, at time.Time) []string {
	base := fmt.Sprintf("vecdex:v3:credit-usage:{%x:%d}", sha256.Sum256([]byte(provider)), partition)
	return []string{base + ":day:" + at.UTC().Format("2006-01-02"), base + ":month:" + at.UTC().Format("2006-01"),
		base + ":total", base + ":pool:" + grant, base + ":live:" + grant, base + ":request:" + grant + ":" + id}
}

func creditPoolCommand(ticket *domusage.CreditTicket, action string) *db.CreditPoolCommand {
	keys := creditUsageKeys(ticket.Provider, ticket.GrantID, ticket.ID, ticket.Partition, time.UnixMilli(ticket.At))
	return &db.CreditPoolCommand{Action: action, GrantID: ticket.GrantID, PoolKey: keys[3], LiveKey: keys[4],
		ReceiptKey: keys[5], CounterKeys: keys[:3], Amount: ticket.Amount, ExpiresAt: ticket.ExpiresAt}
}

func (s *Accounting) creditCacheFor(provider string, now time.Time, daily, monthly int64) *creditCache {
	s.mu.Lock()
	defer s.mu.Unlock()
	policy := creditPolicy{provider, now.UTC().Format("2006-01-02"), daily, monthly}
	for key := range s.caches {
		if key.provider == provider && key.day != policy.day {
			delete(s.caches, key)
		}
	}
	cache := s.caches[policy]
	if cache == nil {
		cache = &creditCache{gate: make(chan struct{}, 1)}
		s.caches[policy] = cache
	}
	return cache
}

func (c *creditCache) liveGrants(now int64) []*cachedCreditGrant {
	c.mu.Lock()
	defer c.mu.Unlock()
	live := c.grants[:0]
	for _, grant := range c.grants {
		if grant.grant.ExpiresAt > now {
			live = append(live, grant)
		}
	}
	c.grants = live
	return append([]*cachedCreditGrant(nil), live...)
}

func (c *creditCache) publish(grant db.CreditGrant, firstReservation int64) {
	cached := &cachedCreditGrant{grant: grant, pools: make([]cachedCreditPool, len(grant.Slices))}
	for i, slice := range grant.Slices {
		cached.pools[i].partition = slice.Partition
		cached.pools[i].remaining.Store(slice.Amount)
	}
	cached.pools[0].remaining.Add(-firstReservation)
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.grants) >= creditCacheGrantLimit {
		c.grants = c.grants[len(c.grants)-creditCacheGrantLimit+1:]
	}
	c.grants = append(c.grants, cached)
}

func claimCreditHint(pool *cachedCreditPool, amount int64) bool {
	for !pool.closed.Load() {
		available := pool.remaining.Load()
		if available < amount {
			return false
		}
		if pool.remaining.CompareAndSwap(available, available-amount) {
			return true
		}
	}
	return false
}
