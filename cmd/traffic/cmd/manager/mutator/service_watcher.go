package mutator

import (
	"context"
	"strings"

	"github.com/puzpuzpuz/xsync/v4"
	core "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/tools/cache"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/managerutil"
	"github.com/telepresenceio/telepresence/v2/pkg/agentconfig"
	"github.com/telepresenceio/telepresence/v2/pkg/agentmap"
	"github.com/telepresenceio/telepresence/v2/pkg/informer"
	"github.com/telepresenceio/telepresence/v2/pkg/k8sapi"
)

type affectedConfig struct {
	err error
	wl  k8sapi.Workload // If a workload is retrieved, it will be cached here.
	sc  *agentconfig.Sidecar
}

func (c *configWatcher) configsAffectedBySvc(ctx context.Context, svc *core.Service, trustUID bool) []affectedConfig {
	references := func(ac *agentconfig.Sidecar) (k8sapi.Workload, error, bool) {
		for _, cn := range ac.Containers {
			for _, ic := range cn.Intercepts {
				if ic.ServiceUID == svc.UID {
					return nil, nil, true
				}
			}
		}
		if trustUID {
			// A deleted service will only affect configs that matches its UID
			return nil, nil, false
		}

		// The config will be affected if a service is added or modified so that it now selects the pod for the workload.
		wl, err := agentmap.GetWorkload(ctx, ac.WorkloadName, ac.Namespace, ac.WorkloadKind)
		if err != nil {
			return nil, err, false
		}
		return wl, nil, labels.SelectorFromSet(svc.Spec.Selector).Matches(labels.Set(wl.GetPodTemplate().Labels))
	}

	var affected []affectedConfig
	c.agentConfigs.Compute(svc.Namespace, func(scMap map[string]*agentconfig.Sidecar, loaded bool) (map[string]*agentconfig.Sidecar, xsync.ComputeOp) {
		if loaded {
			for _, sc := range scMap {
				if wl, err, ok := references(sc); ok {
					affected = append(affected, affectedConfig{sc: sc, wl: wl, err: err})
				}
			}
		}
		return nil, xsync.CancelOp
	})
	return affected
}

func (c *configWatcher) affectedConfigs(ctx context.Context, svc *core.Service, trustUID bool) []affectedConfig {
	return c.configsAffectedBySvc(ctx, svc, trustUID)
}

func (c *configWatcher) startServices(ctx context.Context, ns string) cache.SharedIndexInformer {
	f := informer.GetK8sFactory(ctx, ns)
	ix := f.Core().V1().Services().Informer()
	_ = ix.SetTransform(func(o any) (any, error) {
		// Strip of the parts of the service that we don't care about
		if svc, ok := o.(*core.Service); ok {
			svc.ManagedFields = nil
			svc.Status = core.ServiceStatus{}
			svc.Finalizers = nil
			svc.OwnerReferences = nil
		}
		return o, nil
	})
	_ = ix.SetWatchErrorHandler(func(_ *cache.Reflector, err error) {
		dlog.Errorf(ctx, "watcher for Services %s: %v", whereWeWatch(ns), err)
	})
	return ix
}

func (c *configWatcher) watchServices(ctx context.Context, ix cache.SharedIndexInformer) (cache.ResourceEventHandlerRegistration, error) {
	return ix.AddEventHandler(
		cache.ResourceEventHandlerFuncs{
			AddFunc: func(obj any) {
				if svc, ok := obj.(*core.Service); ok {
					c.updateSvc(ctx, svc, false)
				}
			},
			DeleteFunc: func(obj any) {
				if svc, ok := obj.(*core.Service); ok {
					c.updateSvc(ctx, svc, true)
				} else if dfsu, ok := obj.(*cache.DeletedFinalStateUnknown); ok {
					if svc, ok := dfsu.Obj.(*core.Service); ok {
						c.updateSvc(ctx, svc, true)
					}
				}
			},
			UpdateFunc: func(oldObj, newObj any) {
				if newSvc, ok := newObj.(*core.Service); ok {
					c.updateSvc(ctx, newSvc, true)
				}
			},
		})
}

func (c *configWatcher) updateSvc(ctx context.Context, svc *core.Service, trustUID bool) {
	// Does the snapshot contain workloads that we didn't find using the service's Spec.Selector?
	// If so, include them, or if workload for the config entry isn't found, delete that entry
	img := managerutil.GetAgentImage(ctx)
	if img == "" {
		return
	}
	cfg, err := managerutil.GetEnv(ctx).GeneratorConfig(img)
	if err != nil {
		dlog.Error(ctx, err)
		return
	}
	for _, ax := range c.affectedConfigs(ctx, svc, trustUID) {
		ac := ax.sc
		wl := ax.wl
		if wl == nil {
			err = ax.err
			if err == nil {
				wl, err = agentmap.GetWorkload(ctx, ac.WorkloadName, ac.Namespace, ac.WorkloadKind)
			}
			if err != nil {
				if errors.IsNotFound(err) {
					dlog.Debugf(ctx, "Deleting config entry for %s", wl)
					c.Delete(ac.AgentName, ac.Namespace)
				} else {
					dlog.Error(ctx, err)
				}
				continue
			}
		}
		dlog.Debugf(ctx, "Regenerating config entry for %s", wl)
		acn, err := cfg.Generate(ctx, wl, ac)
		if err != nil {
			if strings.Contains(err.Error(), "unable to find") {
				c.Delete(ac.AgentName, ac.Namespace)
			} else {
				dlog.Error(ctx, err)
			}
			continue
		}
		c.Store(acn)
		dlog.Debugf(ctx, "deleting pods with config mismatch for %s", wl)
		err = c.EvictPodsWithAgentConfigMismatch(ctx, wl, acn)
		if err != nil {
			dlog.Error(ctx, err)
		}
	}
}
