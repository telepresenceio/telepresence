package informer

import (
	"context"

	"k8s.io/client-go/informers"

	"github.com/datawire/k8sapi/pkg/k8sapi"
)

type factoryKey string

func WithFactory(ctx context.Context, ns string) context.Context {
	var opts []informers.SharedInformerOption
	if ns != "" {
		opts = append(opts, informers.WithNamespace(ns))
	}
	factory := informers.NewSharedInformerFactoryWithOptions(k8sapi.GetK8sInterface(ctx), 0, opts...)
	return context.WithValue(ctx, factoryKey(ns), factory)
}

func GetFactory(ctx context.Context, ns string) informers.SharedInformerFactory {
	if f, ok := ctx.Value(factoryKey(ns)).(informers.SharedInformerFactory); ok {
		return f
	}
	// Check if cluster-global a factory is available, unless that was what was
	// originally requested.
	if ns != "" {
		if f, ok := ctx.Value(factoryKey("")).(informers.SharedInformerFactory); ok {
			return f
		}
	}
	return nil
}
