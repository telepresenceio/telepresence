package dlog

import (
	"context"
)

func WithLogger(ctx context.Context, logger Logger) context.Context {
	return context.WithValue(ctx, loggerContextKey{}, logger)
}

func GetLogger(ctx context.Context) Logger {
	return ctx.Value(loggerContextKey{}).(Logger)
}

type loggerContextKey struct{}
