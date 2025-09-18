package trafficmgr

import (
	"context"
)

type sessionKey struct{}

func withSession(ctx context.Context, session *session) context.Context {
	return context.WithValue(ctx, sessionKey{}, session)
}

func getSession(ctx context.Context) *session {
	if s, ok := ctx.Value(sessionKey{}).(*session); ok {
		return s
	}
	return nil
}
