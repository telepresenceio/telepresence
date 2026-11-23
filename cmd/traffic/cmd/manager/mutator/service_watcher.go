package mutator

import (
	"context"
	"errors"
	"net/netip"
	"strings"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"google.golang.org/protobuf/types/known/durationpb"

	core "k8s.io/api/core/v1"
	k8sErrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/tools/cache"

	"github.com/telepresenceio/clog"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/managerutil"
	"github.com/telepresenceio/telepresence/v2/pkg/agentconfig"
	"github.com/telepresenceio/telepresence/v2/pkg/agentmap"
	"github.com/telepresenceio/telepresence/v2/pkg/informer"
	"github.com/telepresenceio/telepresence/v2/pkg/k8sapi"
)

type affectedConfig struct {
	err error
	// claimed is true when an intercept of the config already refers to the Service.
	claimed bool
	wl      k8sapi.Workload // If a workload is retrieved, it will be cached here.
	sc      *agentconfig.Sidecar
}

func (c *configWatcher) configsAffectedBySvc(ctx context.Context, svc *core.Service, includeSelectorMatches bool) []affectedConfig {
	claims := func(ac *agentconfig.Sidecar) bool {
		for _, cn := range ac.Containers {
			for _, ic := range cn.Intercepts {
				if ic.ServiceUID == svc.UID {
					return true
				}
			}
		}
		return false
	}
	references := func(ac *agentconfig.Sidecar) (k8sapi.Workload, error, bool) {
		if claims(ac) {
			return nil, nil, true
		}
		if !includeSelectorMatches {
			// A deleted service will only affect configs that matches its UID
			return nil, nil, false
		}
		if len(svc.Spec.Selector) == 0 {
			// A selectorless Service does not select pods by label. Existing
			// UID claimants above still need regeneration when a selector is
			// removed, but it must not make every workload in the namespace
			// look newly selected.
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
	if scMap, loaded := c.agentConfigs.Load(svc.Namespace); loaded {
		scMap.Range(func(_ string, sc *agentconfig.Sidecar) bool {
			if wl, err, ok := references(sc); ok {
				affected = append(affected, affectedConfig{sc: sc, wl: wl, err: err, claimed: claims(sc)})
			}
			return true
		})
	}
	return affected
}

func (c *configWatcher) affectedConfigs(ctx context.Context, svc *core.Service, includeSelectorMatches bool) []affectedConfig {
	return c.configsAffectedBySvc(ctx, svc, includeSelectorMatches)
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
		clog.Errorf(ctx, "watcher for Services %s: %v", whereWeWatch(ns), err)
	})
	return ix
}

func (c *configWatcher) watchServices(ctx context.Context, ix cache.SharedIndexInformer) (cache.ResourceEventHandlerRegistration, error) {
	return ix.AddEventHandler(
		cache.ResourceEventHandlerFuncs{
			AddFunc: func(obj any) {
				defer recoverHandlerPanic(ctx, "service add")
				if svc, ok := obj.(*core.Service); ok {
					c.updateSvc(ctx, svc, true)
				}
			},
			DeleteFunc: func(obj any) {
				defer recoverHandlerPanic(ctx, "service delete")
				if svc, ok := obj.(*core.Service); ok {
					c.updateSvc(ctx, svc, false)
				} else if dfsu, ok := obj.(*cache.DeletedFinalStateUnknown); ok {
					if svc, ok := dfsu.Obj.(*core.Service); ok {
						c.updateSvc(ctx, svc, false)
					}
				}
			},
			UpdateFunc: func(oldObj, newObj any) {
				defer recoverHandlerPanic(ctx, "service update")
				if newSvc, ok := newObj.(*core.Service); ok {
					c.updateSvc(ctx, newSvc, true)
				}
			},
		})
}

func (c *configWatcher) updateSvc(ctx context.Context, svc *core.Service, includeSelectorMatches bool) {
	// Does the snapshot contain workloads that we didn't find using the service's Spec.Selector?
	// If so, include them, or if workload for the config entry isn't found, delete that entry
	img := managerutil.GetAgentImage(ctx)
	if img == "" {
		return
	}
	cfg, err := managerutil.GetEnv(ctx).GeneratorConfig(img)
	if err != nil {
		clog.Error(ctx, err)
		return
	}
	for _, ax := range c.affectedConfigs(ctx, svc, includeSelectorMatches) {
		ac := ax.sc
		wl := ax.wl
		if wl == nil {
			err = ax.err
			if err == nil {
				wl, err = agentmap.GetWorkload(ctx, ac.WorkloadName, ac.Namespace, ac.WorkloadKind)
			}
			if err != nil {
				if k8sErrors.IsNotFound(err) {
					clog.Debugf(ctx, "Deleting config entry for %s", wl)
					c.Delete(ac.AgentName, ac.Namespace)
				} else {
					clog.Error(ctx, err)
				}
				continue
			}
		}
		clog.Debugf(ctx, "Regenerating config entry for %s", wl)
		acn, err := cfg.Generate(ctx, wl, ac)
		if err != nil {
			if strings.Contains(err.Error(), "unable to find") {
				c.Delete(ac.AgentName, ac.Namespace)
			} else {
				clog.Error(ctx, err)
			}
			continue
		}
		c.applyRegeneratedConfig(ctx, wl, ac, acn, ax.claimed)
	}
}

// applyRegeneratedConfig stores the config that a Service event produced. Pods are evicted only
// when an intercept of the old config already referred to the Service and the config changed. A
// config that changed merely because a Service now matches the workload takes effect when a pod
// starts or a client attaches.
func (c *configWatcher) applyRegeneratedConfig(ctx context.Context, wl k8sapi.Workload, old, regenerated *agentconfig.Sidecar, claimed bool) {
	c.Store(regenerated)
	if !claimed || sameAgentConfig(ctx, old, regenerated) {
		return
	}
	clog.Debugf(ctx, "deleting pods with config mismatch for %s", wl)
	if err := c.EvictPodsWithAgentConfigMismatch(ctx, wl, regenerated); err != nil {
		clog.Error(ctx, err)
	}
}

// sameAgentConfig reports whether the two configs are equal as a pod's annotation carries them.
func sameAgentConfig(ctx context.Context, a, b *agentconfig.Sidecar) bool {
	ta, errA := tightAgentConfig(a)
	tb, errB := tightAgentConfig(b)
	if errA != nil || errB != nil {
		clog.Errorf(ctx, "unable to marshal agent config: %v", errors.Join(errA, errB))
		return false
	}
	return cmp.Equal(ta, tb, cmpopts.EquateEmpty(), cmpopts.EquateComparable(netip.Prefix{}), cmp.Comparer(func(x, y *durationpb.Duration) bool {
		return x.AsDuration() == y.AsDuration()
	}))
}
