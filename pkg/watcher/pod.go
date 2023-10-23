package watcher

import (
	"context"

	core "k8s.io/api/core/v1"

	"github.com/datawire/k8sapi/pkg/k8sapi"
)

func Pods(ctx context.Context, namespaces []string) Watcher[*core.Pod] {
	if len(namespaces) == 0 {
		namespaces = []string{""}
	}
	eps := make([]EventProducerInfo, len(namespaces))
	for i, ns := range namespaces {
		epi := &eps[i]
		epi.Producer = k8sapi.GetK8sInterface(ctx).CoreV1().Pods(ns)
		epi.Resource = "pods"
		epi.Namespace = ns
	}
	return NewWatcher[*core.Pod](eps...)
}

type podsKey struct{}

func WithPods(ctx context.Context, watcher Watcher[*core.Pod]) context.Context {
	return context.WithValue(ctx, podsKey{}, watcher)
}

func GetPods(ctx context.Context) Watcher[*core.Pod] {
	if pw, ok := ctx.Value(podsKey{}).(Watcher[*core.Pod]); ok {
		return pw
	}
	return nil
}
