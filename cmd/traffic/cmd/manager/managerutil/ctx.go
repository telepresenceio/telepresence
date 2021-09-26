package managerutil

import (
	"context"

	"k8s.io/client-go/kubernetes"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
)

func WithSessionInfo(ctx context.Context, si *manager.SessionInfo) context.Context {
	if id := si.GetSessionId(); id != "" {
		return WithSessionID(ctx, id)
	}
	return ctx
}

func WithSessionID(ctx context.Context, sessionID string) context.Context {
	ctx = context.WithValue(ctx, sessionContextKey{}, sessionID)
	ctx = dlog.WithField(ctx, "session_id", sessionID)
	return ctx
}

func GetSessionID(ctx context.Context) string {
	id := ctx.Value(sessionContextKey{})
	if id == nil {
		return ""
	}
	return id.(string)
}

type sessionContextKey struct{}

func WithK8SClientset(ctx context.Context, clientset kubernetes.Interface) context.Context {
	return context.WithValue(ctx, clientsetKey{}, clientset)
}

func GetK8sClientset(ctx context.Context) kubernetes.Interface {
	clientset, ok := ctx.Value(clientsetKey{}).(kubernetes.Interface)
	if !ok {
		return nil
	}
	return clientset
}

type clientsetKey struct{}
