package usage

import (
	"context"
	"fmt"
)

// Counters separates measured tokens from conservative reservations.
type Counters struct {
	Tokens   int64
	Reserved int64
	Requests int64
	Unknown  int64
}

// Snapshot contains independent current-day, current-month and lifetime counters.
type Snapshot struct{ Day, Month, Total Counters }

// InvocationObserver reserves capacity before an upstream invocation. The returned
// completion function must be called once, including on timeout or malformed replies.
type InvocationObserver interface {
	Begin(ctx context.Context, inputs int) (complete func(tokens int, unknown bool), err error)
}

// BeginInvocation supplies a no-op completion when accounting is not configured.
func BeginInvocation(ctx context.Context, observer InvocationObserver, inputs int) (func(int, bool), error) {
	if observer == nil {
		return func(int, bool) {}, nil
	}
	complete, err := observer.Begin(ctx, inputs)
	if err != nil {
		return nil, fmt.Errorf("reserve invocation: %w", err)
	}
	return complete, nil
}
