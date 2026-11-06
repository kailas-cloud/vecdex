package domain

import (
	"context"
	"sync/atomic"
)

type writeOutcomeKey struct{}

// WriteOutcome observes receipt replay for one source mutation. It does not
// imply the current source still exists or matches the historical receipt.
type WriteOutcome struct{ replayed atomic.Bool }

// WithWriteOutcome attaches an independent observer for one mutation call.
// The observer is safe to read after the call and cannot be supplied by clients.
func WithWriteOutcome(ctx context.Context) (context.Context, *WriteOutcome) {
	outcome := &WriteOutcome{}
	return context.WithValue(ctx, writeOutcomeKey{}, outcome), outcome
}

// Replayed reports that the operation returned its durable idempotency receipt.
func (o *WriteOutcome) Replayed() bool { return o.replayed.Load() }

// MarkWriteReplayed records only a successfully recovered durable receipt.
func MarkWriteReplayed(ctx context.Context) {
	if outcome, ok := ctx.Value(writeOutcomeKey{}).(*WriteOutcome); ok {
		outcome.replayed.Store(true)
	}
}
