package watcher

import (
	"context"

	core "k8s.io/api/core/v1"

	"github.com/datawire/k8sapi/pkg/k8sapi"
)

func Nodes(ctx context.Context) Watcher[*core.Node] {
	return NewWatcher[*core.Node](EventProducerInfo{
		Producer: k8sapi.GetK8sInterface(ctx).CoreV1().Nodes(),
		Resource: "nodes",
	})
}

type nodesKey struct{}

func WithNodes(ctx context.Context, watcher Watcher[*core.Node]) context.Context {
	return context.WithValue(ctx, nodesKey{}, watcher)
}

func GetNodes(ctx context.Context) Watcher[*core.Node] {
	if pw, ok := ctx.Value(nodesKey{}).(Watcher[*core.Node]); ok {
		return pw
	}
	return nil
}
