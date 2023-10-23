package watcher

import (
	"context"

	core "k8s.io/api/core/v1"

	"github.com/datawire/k8sapi/pkg/k8sapi"
)

type Service struct {
	Ports    []core.ServicePort
	Selector map[string]string
}

func Services(ctx context.Context, namespaces []string) Watcher[*core.Service] {
	if len(namespaces) == 0 {
		namespaces = []string{""}
	}
	eps := make([]EventProducerInfo, len(namespaces))
	for i, ns := range namespaces {
		epi := &eps[i]
		epi.Producer = k8sapi.GetK8sInterface(ctx).CoreV1().Services(ns)
		epi.Resource = "services"
		epi.Namespace = ns
	}
	return NewWatcher[*core.Service](eps...)
}

type servicesKey struct{}

func WithServices(ctx context.Context, watcher Watcher[*core.Service]) context.Context {
	return context.WithValue(ctx, servicesKey{}, watcher)
}

func GetServices(ctx context.Context) Watcher[*core.Service] {
	if sw, ok := ctx.Value(servicesKey{}).(Watcher[*core.Service]); ok {
		return sw
	}
	return nil
}
