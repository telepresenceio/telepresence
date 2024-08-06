package informer

import (
	"context"

	"k8s.io/client-go/informers"

	argorolloutsinformer "github.com/datawire/argo-rollouts-go-client/pkg/client/informers/externalversions"
	"github.com/datawire/k8sapi/pkg/k8sapi"
)

type factoryKey string

func getOpts(ns string) (k8sOpts []informers.SharedInformerOption, argoOpts []argorolloutsinformer.SharedInformerOption) {
	if ns != "" {
		k8sOpts = append(k8sOpts, informers.WithNamespace(ns))
		argoOpts = append(argoOpts, argorolloutsinformer.WithNamespace(ns))
	}

	return k8sOpts, argoOpts
}

func WithFactory(ctx context.Context, ns string) context.Context {
	k8sOpts, argoOpts := getOpts(ns)
	i := k8sapi.GetJoinedClientSetInterface(ctx)
	k8sFactory := informers.NewSharedInformerFactoryWithOptions(i, 0, k8sOpts...)
	argoRolloutFactory := argorolloutsinformer.NewSharedInformerFactoryWithOptions(i, 0, argoOpts...)
	return context.WithValue(ctx, factoryKey(ns), NewDefaultGlobalFactory(k8sFactory, argoRolloutFactory))
}

func GetFactory(ctx context.Context, ns string) GlobalFactory {
	if f, ok := ctx.Value(factoryKey(ns)).(GlobalFactory); ok {
		return f
	}
	// Check if cluster-global a factory is available, unless that was what was
	// originally requested.
	if ns != "" {
		if f, ok := ctx.Value(factoryKey("")).(GlobalFactory); ok {
			return f
		}
	}
	return nil
}

func GetK8sFactory(ctx context.Context, ns string) informers.SharedInformerFactory {
	f := GetFactory(ctx, ns)
	if f != nil {
		return f.GetK8sInformerFactory()
	}
	return nil
}

func GetArgoRolloutsFactory(ctx context.Context, ns string) argorolloutsinformer.SharedInformerFactory {
	f := GetFactory(ctx, ns)
	if f != nil {
		return f.GetArgoRolloutsInformerFactory()
	}
	return nil
}
