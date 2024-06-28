package mutator

import (
	"context"

	apps "k8s.io/api/apps/v1"
	"k8s.io/client-go/tools/cache"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/pkg/informer"
)

func (c *configWatcher) startDeployments(ctx context.Context, ns string) cache.SharedIndexInformer {
	f := informer.GetFactory(ctx, ns)
	ix := f.Apps().V1().Deployments().Informer()
	_ = ix.SetTransform(func(o any) (any, error) {
		// Strip of the parts of the deployment that we don't care about. Saves memory
		if dep, ok := o.(*apps.Deployment); ok {
			dep.ManagedFields = nil
			dep.Status = apps.DeploymentStatus{}
			dep.Finalizers = nil
			dep.OwnerReferences = nil
		}
		return o, nil
	})
	_ = ix.SetWatchErrorHandler(func(_ *cache.Reflector, err error) {
		dlog.Errorf(ctx, "watcher for Deployments %s: %v", whereWeWatch(ns), err)
	})
	return ix
}
