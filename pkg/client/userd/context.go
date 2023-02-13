package userd

import (
	"context"
)

type sessionKey struct{}

func WithSession(ctx context.Context, session Session) context.Context {
	return context.WithValue(ctx, sessionKey{}, session)
}

func GetSession(ctx context.Context) Session {
	if s, ok := ctx.Value(sessionKey{}).(Session); ok {
		return s
	}
	return nil
}
