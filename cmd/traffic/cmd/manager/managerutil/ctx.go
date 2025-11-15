package managerutil

import (
	"context"

	"github.com/telepresenceio/dlib/v2/dlog"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/tunnel"
)

func WithSessionInfo(ctx context.Context, si *manager.SessionInfo) context.Context {
	if id := si.GetSessionId(); id != "" {
		return WithSessionID(ctx, tunnel.SessionID(id))
	}
	return ctx
}

func WithSessionID(ctx context.Context, sessionID tunnel.SessionID) context.Context {
	ctx = context.WithValue(ctx, sessionContextKey{}, sessionID)
	ctx = dlog.WithField(ctx, "session_id", sessionID)
	return ctx
}

func GetSessionID(ctx context.Context) tunnel.SessionID {
	if id, ok := ctx.Value(sessionContextKey{}).(tunnel.SessionID); ok {
		return id
	}
	return ""
}

type sessionContextKey struct{}
