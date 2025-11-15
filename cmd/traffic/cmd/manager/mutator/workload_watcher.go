package mutator

import (
	"context"
	"strings"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/cache"

	"github.com/telepresenceio/dlib/v2/dlog"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/managerutil"
	"github.com/telepresenceio/telepresence/v2/pkg/agentconfig"
	"github.com/telepresenceio/telepresence/v2/pkg/annotation"
	"github.com/telepresenceio/telepresence/v2/pkg/k8sapi"
	"github.com/telepresenceio/telepresence/v2/pkg/workload"
)

func (c *configWatcher) watchWorkloads(ctx context.Context, ix cache.SharedIndexInformer) (cache.ResourceEventHandlerRegistration, error) {
	return ix.AddEventHandler(
		cache.ResourceEventHandlerFuncs{
			AddFunc: func(obj any) {
				if wl, ok := workload.FromAny(obj); ok && len(wl.GetOwnerReferences()) == 0 {
					c.updateWorkload(ctx, wl, nil, workload.GetWorkloadState(wl))
				}
			},
			DeleteFunc: func(obj any) {
				if wl, ok := workload.FromAny(obj); ok {
					if len(wl.GetOwnerReferences()) == 0 {
						c.Delete(wl.GetName(), wl.GetNamespace())
					}
				} else if dfsu, ok := obj.(*cache.DeletedFinalStateUnknown); ok {
					if wl, ok = workload.FromAny(dfsu.Obj); ok && len(wl.GetOwnerReferences()) == 0 {
						c.Delete(wl.GetName(), wl.GetNamespace())
					}
				}
			},
			UpdateFunc: func(oldObj, newObj any) {
				if wl, ok := workload.FromAny(newObj); ok && len(wl.GetOwnerReferences()) == 0 {
					if oldWl, ok := workload.FromAny(oldObj); ok {
						c.updateWorkload(ctx, wl, oldWl, workload.GetWorkloadState(wl))
					}
				}
			},
		})
}

func (c *configWatcher) updateWorkload(ctx context.Context, wl, oldWl k8sapi.Workload, state workload.State) {
	if state == workload.StateFailure {
		return
	}
	tpl := wl.GetPodTemplate()
	ia := annotation.GetAnnotation(ctx, tpl.Annotations, annotation.InjectTrafficAgent, annotation.LegacyInjectTrafficAgent)
	if ia == "" {
		return
	}
	if oldWl != nil && cmp.Equal(oldWl.GetPodTemplate(), tpl,
		cmpopts.IgnoreFields(meta.ObjectMeta{}, "Namespace", "UID", "ResourceVersion", "CreationTimestamp", "DeletionTimestamp"),
		cmpopts.IgnoreMapEntries(func(k, _ string) bool {
			return k == annotation.RestartedAt
		})) {
		return
	}

	switch ia {
	case "enabled":
		env := managerutil.GetEnv(ctx)
		if !env.EnabledWorkloadKinds.Contains(wl.GetKind()) {
			return
		}
		img := managerutil.GetAgentImage(ctx)
		if img == "" {
			return
		}
		cfg, err := env.GeneratorConfig(img)
		if err != nil {
			dlog.Error(ctx, err)
			return
		}
		var sc *agentconfig.Sidecar
		if oldWl != nil {
			sc = c.Get(wl.GetName(), wl.GetNamespace())
		}
		action := "Generating"
		if sc == nil {
			action = "Regenerating"
		}
		dlog.Debugf(ctx, "%s config entry for %s", action, wl)

		sc, err = cfg.Generate(ctx, wl, sc)
		if err != nil {
			if strings.Contains(err.Error(), "unable to find") {
				c.Delete(wl.GetName(), wl.GetNamespace())
			} else {
				dlog.Error(ctx, err)
			}
			return
		}

		c.Store(sc)
		dlog.Debugf(ctx, "deleting pods with config mismatch for %s", wl)
		err = c.EvictPodsWithAgentConfigMismatch(ctx, wl, sc)
		if err != nil {
			dlog.Error(ctx, err)
		}
	case "false", "disabled":
		c.Delete(wl.GetName(), wl.GetNamespace())
		err := c.EvictPodsWithAgentConfig(ctx, wl)
		if err != nil {
			dlog.Error(ctx, err)
		}
	}
}
