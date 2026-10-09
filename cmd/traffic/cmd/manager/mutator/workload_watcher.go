package mutator

import (
	"context"
	"strings"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/cache"

	"github.com/telepresenceio/clog"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/managerutil"
	"github.com/telepresenceio/telepresence/v2/pkg/agentconfig"
	"github.com/telepresenceio/telepresence/v2/pkg/annotation"
	"github.com/telepresenceio/telepresence/v2/pkg/k8sapi"
	"github.com/telepresenceio/telepresence/v2/pkg/workload"
)

// shouldWatchWorkload keeps ReplicaSets owned by custom controllers visible to
// the mutator. Other controller-owned workload kinds keep the existing
// top-level-only behavior. ReplicaSets controlled by an enabled Deployment or
// Rollout are reconciled through that parent workload instead.
func shouldWatchWorkload(wl k8sapi.Workload, enabledKinds k8sapi.Kinds) bool {
	if len(wl.GetOwnerReferences()) == 0 {
		return true
	}
	if wl.GetKind() != k8sapi.ReplicaSetKind {
		return false
	}
	for _, ref := range wl.GetOwnerReferences() {
		if ref.Controller == nil || !*ref.Controller {
			continue
		}
		kind := k8sapi.Kind(ref.Kind)
		switch kind {
		case k8sapi.DeploymentKind, k8sapi.RolloutKind:
			return !enabledKinds.Contains(kind)
		default:
			return true
		}
	}
	return false
}

func (c *configWatcher) watchWorkloads(ctx context.Context, ix cache.SharedIndexInformer) (cache.ResourceEventHandlerRegistration, error) {
	enabledKinds := managerutil.GetEnv(ctx).EnabledWorkloadKinds
	deleteWorkload := func(wl k8sapi.Workload) {
		c.Delete(wl.GetName(), wl.GetNamespace())
		c.deleteEvictionState(WorkloadKey{
			Name:      wl.GetName(),
			Namespace: wl.GetNamespace(),
			Kind:      wl.GetKind(),
		})
	}
	return ix.AddEventHandler(
		cache.ResourceEventHandlerFuncs{
			AddFunc: func(obj any) {
				if wl, ok := workload.FromAny(obj); ok && shouldWatchWorkload(wl, enabledKinds) {
					c.updateWorkload(ctx, wl, nil, workload.GetWorkloadState(wl))
				}
			},
			DeleteFunc: func(obj any) {
				if wl, ok := workload.FromAny(obj); ok {
					if shouldWatchWorkload(wl, enabledKinds) {
						deleteWorkload(wl)
					}
				} else if dfsu, ok := obj.(*cache.DeletedFinalStateUnknown); ok {
					if wl, ok = workload.FromAny(dfsu.Obj); ok && shouldWatchWorkload(wl, enabledKinds) {
						deleteWorkload(wl)
					}
				}
			},
			UpdateFunc: func(oldObj, newObj any) {
				if wl, ok := workload.FromAny(newObj); ok && shouldWatchWorkload(wl, enabledKinds) {
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
		if !workloadUpdateInProgress(oldWl) || workloadUpdateInProgress(wl) {
			return
		}
		clog.Debugf(ctx, "Reconciling agent config after %s finished updating", wl)
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
			clog.Error(ctx, err)
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
		clog.Debugf(ctx, "%s config entry for %s", action, wl)

		sc, err = cfg.Generate(ctx, wl, sc)
		if err != nil {
			if strings.Contains(err.Error(), "unable to find") {
				c.Delete(wl.GetName(), wl.GetNamespace())
			} else {
				clog.Error(ctx, err)
			}
			return
		}

		c.Store(sc)
		clog.Debugf(ctx, "deleting pods with config mismatch for %s", wl)
		err = c.EvictPodsWithAgentConfigMismatch(ctx, wl, sc)
		if err != nil {
			clog.Error(ctx, err)
		}
	case "false", "disabled":
		c.Delete(wl.GetName(), wl.GetNamespace())
		err := c.EvictPodsWithAgentConfig(ctx, wl)
		if err != nil {
			clog.Error(ctx, err)
		}
	}
}
