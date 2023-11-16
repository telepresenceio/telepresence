package scout

import (
	"context"

	"github.com/blang/semver"
)

type sessionKey struct{}

type session interface {
	ManagerVersion() semver.Version
	Done() <-chan struct{}
}

func GetSession(ctx context.Context) session {
	if s, ok := ctx.Value(sessionKey{}).(session); ok {
		return s
	}
	return nil
}

func WithSession(ctx context.Context, s session) context.Context {
	return context.WithValue(ctx, sessionKey{}, s)
}

func sessionReportMutator(ctx context.Context, e []Entry) []Entry {
	// check if client is present in context
	session := GetSession(ctx)
	if session == nil {
		return e
	}

	select {
	// session is dead
	case <-session.Done():
	default:
		v := session.ManagerVersion()
		e = append(e, Entry{
			Key:   "manager_version",
			Value: v.String(),
		})
	}

	return e
}
