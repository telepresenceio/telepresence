package mutator

import (
	"context"
	"strings"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	apps "k8s.io/api/apps/v1"
	core "k8s.io/api/core/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/cache"

	"github.com/datawire/dlib/dlog"
	"github.com/datawire/k8sapi/pkg/k8sapi"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/managerutil"
	"github.com/telepresenceio/telepresence/v2/pkg/agentconfig"
	"github.com/telepresenceio/telepresence/v2/pkg/agentmap"
	"github.com/telepresenceio/telepresence/v2/pkg/informer"
)

func (c *configWatcher) startDeployments(ctx context.Context, ns string) cache.SharedIndexInformer {
	f := informer.GetFactory(ctx, ns)
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
		dlog.Errorf(ctx, "watcher for Deployments %s: %v", whereWeWatch(ns), err)
	})
	return ix
}

func (c *configWatcher) startReplicaSets(ctx context.Context, ns string) cache.SharedIndexInformer {
	f := informer.GetFactory(ctx, ns)
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
		dlog.Errorf(ctx, "watcher for ReplicaSets %s: %v", whereWeWatch(ns), err)
	})
	return ix
}

func (c *configWatcher) startStatefulSets(ctx context.Context, ns string) cache.SharedIndexInformer {
	f := informer.GetFactory(ctx, ns)
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
		dlog.Errorf(ctx, "watcher for StatefulSet %s: %v", whereWeWatch(ns), err)
	})
	return ix
}

func workloadFromAny(obj any) (k8sapi.Workload, bool) {
	if ro, ok := obj.(runtime.Object); ok {
		if wl, err := k8sapi.WrapWorkload(ro); err == nil {
			return wl, true
		}
	}
	return nil, false
}

func (c *configWatcher) watchWorkloads(ctx context.Context, ix cache.SharedIndexInformer) error {
	_, err := ix.AddEventHandler(
		cache.ResourceEventHandlerFuncs{
			AddFunc: func(obj any) {
				if wl, ok := workloadFromAny(obj); ok && len(wl.GetOwnerReferences()) == 0 {
					c.updateWorkload(ctx, wl, nil, workloadState(wl))
				}
			},
			DeleteFunc: func(obj any) {
				if wl, ok := workloadFromAny(obj); ok {
					if len(wl.GetOwnerReferences()) == 0 {
						c.deleteWorkload(ctx, wl)
					}
				} else if dfsu, ok := obj.(*cache.DeletedFinalStateUnknown); ok {
					if wl, ok = workloadFromAny(dfsu.Obj); ok && len(wl.GetOwnerReferences()) == 0 {
						c.deleteWorkload(ctx, wl)
					}
				}
			},
			UpdateFunc: func(oldObj, newObj any) {
				if wl, ok := workloadFromAny(newObj); ok && len(wl.GetOwnerReferences()) == 0 {
					if oldWl, ok := workloadFromAny(oldObj); ok {
						c.updateWorkload(ctx, wl, oldWl, workloadState(wl))
					}
				}
			},
		})
	if err == nil {
		// Act on initial snapshot
		for _, obj := range ix.GetStore().List() {
			if wl, ok := workloadFromAny(obj); ok && len(wl.GetOwnerReferences()) == 0 {
				c.updateWorkload(ctx, wl, nil, workloadState(wl))
			}
		}
	}
	return err
}

func (c *configWatcher) deleteWorkload(ctx context.Context, wl k8sapi.Workload) {
	scx, err := c.Get(ctx, wl.GetName(), wl.GetNamespace())
	if err != nil {
		dlog.Errorf(ctx, "Failed to get sidecar config: %v", err)
	} else if scx != nil {
		err = c.Delete(ctx, wl.GetName(), wl.GetNamespace())
		if err != nil {
			dlog.Errorf(ctx, "Failed to delete sidecar config: %v", err)
		}
	}
}

func (c *configWatcher) updateWorkload(ctx context.Context, wl, oldWl k8sapi.Workload, state WorkloadState) {
	if state == WorkloadStateFailure {
		return
	}
	tpl := wl.GetPodTemplate()
	ia, ok := tpl.Annotations[InjectAnnotation]
	if !ok {
		return
	}
	if oldWl != nil {
		diff := cmp.Diff(oldWl.GetPodTemplate(), tpl,
			cmpopts.IgnoreFields(meta.ObjectMeta{}, "Namespace", "UID", "ResourceVersion", "CreationTimestamp", "DeletionTimestamp"),
			cmpopts.IgnoreMapEntries(func(k, _ string) bool {
				return k == annRestartedAt
			}),
		)
		if diff == "" {
			return
		}
		dlog.Debugf(ctx, "Diff: %s", diff)
	}
	switch ia {
	case "enabled":
		img := managerutil.GetAgentImage(ctx)
		if img == "" {
			return
		}
		cfg, err := agentmap.GeneratorConfigFunc(img)
		if err != nil {
			dlog.Error(ctx, err)
			return
		}
		var scx agentconfig.SidecarExt
		if oldWl != nil {
			scx, err = c.Get(ctx, wl.GetName(), wl.GetNamespace())
			if err != nil {
				dlog.Errorf(ctx, "Failed to get sidecar config: %v", err)
				return
			}
		}
		action := "Generating"
		if scx == nil {
			action = "Regenerating"
		}
		dlog.Debugf(ctx, "%s config entry for %s %s.%s", action, wl.GetKind(), wl.GetName(), wl.GetNamespace())

		scx, err = cfg.Generate(ctx, wl, scx)
		if err != nil {
			if strings.Contains(err.Error(), "unable to find") {
				if err = c.remove(ctx, wl.GetName(), wl.GetNamespace()); err != nil {
					dlog.Error(ctx, err)
				}
			} else {
				dlog.Error(ctx, err)
			}
		}
		if err = c.store(ctx, scx); err != nil {
			dlog.Error(ctx, err)
		}
	case "false", "disabled":
		c.deleteWorkload(ctx, wl)
	}
}
