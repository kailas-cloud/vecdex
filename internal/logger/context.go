package logger

import (
	"context"

	"go.uber.org/zap"
)

type ctxKey struct{}

// ContextWithLogger stores a logger in the context.
func ContextWithLogger(ctx context.Context, logger *zap.Logger) context.Context {
	return context.WithValue(ctx, ctxKey{}, logger)
}

// FromContext extracts a logger from the context.
// Returns zap.NewNop() if no logger is found.
func FromContext(ctx context.Context) *zap.Logger {
	if l, ok := ctx.Value(ctxKey{}).(*zap.Logger); ok {
		return l
	}
	return zap.NewNop()
}
