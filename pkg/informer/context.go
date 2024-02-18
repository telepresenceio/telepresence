package informer

import (
	"context"

	"k8s.io/client-go/informers"
	informerCore "k8s.io/client-go/informers/core/v1"

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

func GetServices(ctx context.Context, ns string) informerCore.ServiceInformer {
	if f := GetFactory(ctx, ns); f != nil {
		return f.Core().V1().Services()
	}
	return nil
}

func GetPods(ctx context.Context, ns string) informerCore.PodInformer {
	if f := GetFactory(ctx, ns); f != nil {
		return f.Core().V1().Pods()
	}
	return nil
}

func GetNodes(ctx context.Context, ns string) informerCore.NodeInformer {
	if f := GetFactory(ctx, ns); f != nil {
		return f.Core().V1().Nodes()
	}
	return nil
}

func GetConfigMaps(ctx context.Context, ns string) informerCore.ConfigMapInformer {
	if f := GetFactory(ctx, ns); f != nil {
		return f.Core().V1().ConfigMaps()
	}
	return nil
}
