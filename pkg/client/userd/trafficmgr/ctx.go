package trafficmgr

import (
	"context"
)

type sessionKey struct{}

func WithSession(ctx context.Context, session Session) context.Context {
	return context.WithValue(ctx, sessionKey{}, session)
}

func GetSession(ctx context.Context) Session {
	val := ctx.Value(sessionKey{})
	if sess, ok := val.(Session); ok {
		return sess
	}
	return nil
}
