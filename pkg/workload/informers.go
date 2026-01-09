package workload

import (
	"context"

	apps "k8s.io/api/apps/v1"
	core "k8s.io/api/core/v1"
	"k8s.io/client-go/tools/cache"

	argorollouts "github.com/datawire/argo-rollouts-go-client/pkg/apis/rollouts/v1alpha1"
	"github.com/telepresenceio/clog"
	"github.com/telepresenceio/telepresence/v2/pkg/informer"
)

func whereWeWatch(ns string) string {
	if ns == "" {
		return "cluster wide"
	}
	return "in namespace " + ns
}

func StartDeployments(ctx context.Context, ns string) cache.SharedIndexInformer {
	f := informer.GetK8sFactory(ctx, ns)
	ix := f.Apps().V1().Deployments().Informer()
	_ = ix.SetTransform(func(o any) (any, error) {
		// Strip the parts of the deployment that we don't care about to save memory
		if dep, ok := o.(*apps.Deployment); ok {
			om := &dep.ObjectMeta
			if an := om.Annotations; an != nil {
				delete(an, core.LastAppliedConfigAnnotation)
			}
			dep.ManagedFields = nil
			dep.Finalizers = nil
			dep.OwnerReferences = nil
		}
		return o, nil
	})
	_ = ix.SetWatchErrorHandler(func(_ *cache.Reflector, err error) {
		clog.Errorf(ctx, "watcher for Deployments %s: %v", whereWeWatch(ns), err)
	})
	return ix
}

func StartReplicaSets(ctx context.Context, ns string) cache.SharedIndexInformer {
	f := informer.GetK8sFactory(ctx, ns)
	ix := f.Apps().V1().ReplicaSets().Informer()
	_ = ix.SetTransform(func(o any) (any, error) {
		// Strip the parts of the replicaset that we don't care about. Saves memory
		if dep, ok := o.(*apps.ReplicaSet); ok {
			om := &dep.ObjectMeta
			if an := om.Annotations; an != nil {
				delete(an, core.LastAppliedConfigAnnotation)
			}
			dep.ManagedFields = nil
			dep.Finalizers = nil
		}
		return o, nil
	})
	_ = ix.SetWatchErrorHandler(func(_ *cache.Reflector, err error) {
		clog.Errorf(ctx, "watcher for ReplicaSets %s: %v", whereWeWatch(ns), err)
	})
	return ix
}

func StartStatefulSets(ctx context.Context, ns string) cache.SharedIndexInformer {
	f := informer.GetK8sFactory(ctx, ns)
	ix := f.Apps().V1().StatefulSets().Informer()
	_ = ix.SetTransform(func(o any) (any, error) {
		// Strip the parts of the stateful that we don't care about. Saves memory
		if dep, ok := o.(*apps.StatefulSet); ok {
			om := &dep.ObjectMeta
			if an := om.Annotations; an != nil {
				delete(an, core.LastAppliedConfigAnnotation)
			}
			dep.ManagedFields = nil
			dep.Finalizers = nil
		}
		return o, nil
	})
	_ = ix.SetWatchErrorHandler(func(_ *cache.Reflector, err error) {
		clog.Errorf(ctx, "watcher for StatefulSet %s: %v", whereWeWatch(ns), err)
	})
	return ix
}

func StartRollouts(ctx context.Context, ns string) cache.SharedIndexInformer {
	f := informer.GetArgoRolloutsFactory(ctx, ns)
	clog.Infof(ctx, "Watching Rollouts in %s", ns)
	ix := f.Argoproj().V1alpha1().Rollouts().Informer()
	_ = ix.SetTransform(func(o any) (any, error) {
		// Strip the parts of the rollout that we don't care about. Saves memory
		if dep, ok := o.(*argorollouts.Rollout); ok {
			om := &dep.ObjectMeta
			if an := om.Annotations; an != nil {
				delete(an, core.LastAppliedConfigAnnotation)
			}
			dep.ManagedFields = nil
			dep.Finalizers = nil
		}
		return o, nil
	})
	_ = ix.SetWatchErrorHandler(func(_ *cache.Reflector, err error) {
		clog.Errorf(ctx, "watcher for Rollouts %s: %v", whereWeWatch(ns), err)
	})
	return ix
}
