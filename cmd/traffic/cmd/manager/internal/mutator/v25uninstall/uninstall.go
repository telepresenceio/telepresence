package v25uninstall

import (
	"context"
	"sync"
	"time"

	apps "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/datawire/dlib/dlog"
	"github.com/datawire/dlib/dtime"
	"github.com/telepresenceio/telepresence/v2/pkg/agentconfig"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/install"
	"github.com/telepresenceio/telepresence/v2/pkg/k8sapi"
	"github.com/telepresenceio/telepresence/v2/pkg/version"
)

const annTelepresenceActions = agentconfig.DomainPrefix + "actions"

type listWorkloads func(c context.Context, namespace string, labelSelector labels.Set) ([]k8sapi.Workload, error)

// RemoveAgents will remove the agent from all workloads in the given namespace, or in all namespaces
// if the given namespace is the empty string. Errors that occur during this process are logged but not
// considered fatal.
//
// The function returns slice of all workloads that were found to have an
// agent (either by explicitly modified workload, or by annotation enablement) is returned.
func RemoveAgents(ctx context.Context, namespace string) []k8sapi.Workload {
	// Remove the agent from all workloads
	var withAgent []k8sapi.Workload
	var withModifications []k8sapi.Workload
	for _, listFn := range []listWorkloads{k8sapi.Deployments, k8sapi.ReplicaSets, k8sapi.StatefulSets} {
		workloads, err := listFn(ctx, namespace, nil)
		if err != nil {
			if !errors.IsNotFound(err) {
				dlog.Error(ctx, err)
			}
			continue
		}

		for _, wl := range workloads {
			// Assume that the agent was added using the mutating webhook when no actions
			// annotation can be found in the workload.
			if ann := wl.GetAnnotations(); ann != nil {
				if _, ok := ann[annTelepresenceActions]; ok {
					withModifications = append(withModifications, wl)
					withAgent = append(withAgent, wl)
				} else if ann[agentconfig.InjectAnnotation] == "enabled" {
					withAgent = append(withAgent, wl)
				}
			}
		}
	}

	wg := sync.WaitGroup{}
	wg.Add(len(withModifications))
	for _, wl := range withModifications {
		go func(wl k8sapi.Workload) {
			defer wg.Done()
			err := undoModifications(ctx, wl)
			if err == nil {
				err = waitForApply(ctx, wl)
			}
			if err != nil {
				dlog.Error(ctx, err)
			}
		}(wl)
	}
	wg.Wait()
	return withAgent
}

func waitForApply(ctx context.Context, wl k8sapi.Workload) error {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	origGeneration := int64(0)
	if wl != nil {
		origGeneration = wl.GetGeneration()
	}

	var err error
	if rs, ok := k8sapi.ReplicaSetImpl(wl); ok {
		if err = refreshReplicaSet(ctx, rs); err != nil {
			return err
		}
	}
	for {
		dtime.SleepWithContext(ctx, time.Second)
		if err = ctx.Err(); err != nil {
			return err
		}

		if err = wl.Refresh(ctx); err != nil {
			return client.CheckTimeout(ctx, err)
		}
		if wl.Updated(origGeneration) {
			dlog.Debugf(ctx, "%s %s.%s successfully applied", wl.GetKind(), wl.GetName(), wl.GetNamespace())
			return nil
		}
	}
}

// refreshReplicaSet finds pods owned by a given ReplicaSet and deletes them.
// We need this because updating a Replica Set does *not* generate new
// pods if the desired amount already exists.
func refreshReplicaSet(ctx context.Context, rs *apps.ReplicaSet) error {
	pods, err := k8sapi.Pods(ctx, rs.Namespace, rs.Spec.Selector.MatchLabels)
	if err != nil {
		return err
	}

	for _, pod := range pods {
		podImpl, _ := k8sapi.PodImpl(pod)
		for _, ownerRef := range podImpl.OwnerReferences {
			if ownerRef.UID == rs.UID {
				dlog.Debugf(ctx, "Deleting pod %s.%s owned by rs %s", podImpl.Name, podImpl.Namespace, rs.Name)
				if err = pod.Delete(ctx); err != nil {
					dlog.Error(ctx, err)
				}
			}
		}
	}
	return nil
}

func getAnnotation(obj k8sapi.Object, data completeAction) (bool, error) {
	ann := obj.GetAnnotations()
	if ann == nil {
		return false, nil
	}
	ajs, ok := ann[annTelepresenceActions]
	if !ok {
		return false, nil
	}
	if err := data.UnmarshalAnnotation(ajs); err != nil {
		return false, k8sapi.ObjErrorf(obj, "annotations[%q]: unable to parse annotation: %q: %w",
			annTelepresenceActions, ajs, err)
	}

	annV, err := data.TelVersion()
	if err != nil {
		return false, k8sapi.ObjErrorf(obj, "annotations[%q]: unable to parse semantic version %q: %w",
			annTelepresenceActions, ajs, err)
	}
	ourV := version.Structured()

	// Compare major and minor versions. 100% backward compatibility is assumed and greater patch versions are allowed
	if ourV.Major < annV.Major || ourV.Major == annV.Major && ourV.Minor < annV.Minor {
		return false, k8sapi.ObjErrorf(obj, "annotations[%q]: the version in the annotation (%v) is more recent than this binary's version (%v)",
			annTelepresenceActions,
			annV, ourV)
	}
	return true, nil
}

func undoModifications(ctx context.Context, wl k8sapi.Object) error {
	var actions workloadActions
	ok, err := getAnnotation(wl, &actions)
	if !ok {
		return err
	}
	if !ok {
		return k8sapi.ObjErrorf(wl, "agent is not installed")
	}

	if err = actions.Undo(wl); err != nil {
		if install.IsAlreadyUndone(err) {
			dlog.Infof(ctx, "Already uninstalled: %v", err)
		} else {
			return err
		}
	}
	mObj := wl.(meta.ObjectMetaAccessor).GetObjectMeta()
	annotations := mObj.GetAnnotations()
	delete(annotations, annTelepresenceActions)
	if len(annotations) == 0 {
		mObj.SetAnnotations(nil)
	}
	explainUndo(ctx, &actions, wl)

	svc, err := k8sapi.GetService(ctx, actions.ReferencedService, wl.GetNamespace())
	if err != nil && !errors.IsNotFound(err) {
		return err
	}
	if svc != nil {
		if err = undoServiceMods(ctx, svc); err != nil {
			return err
		}
	}
	return wl.Update(ctx)
}

func undoServiceMods(ctx context.Context, svc k8sapi.Object) error {
	var actions svcActions
	ok, err := getAnnotation(svc, &actions)
	if !ok {
		return err
	}
	if err = actions.Undo(svc); err != nil {
		if install.IsAlreadyUndone(err) {
			dlog.Infof(ctx, "Already uninstalled: %v", err)
		} else {
			return err
		}
	}
	anns := svc.GetAnnotations()
	delete(anns, annTelepresenceActions)
	if len(anns) == 0 {
		anns = nil
	}
	svc.SetAnnotations(anns)
	explainUndo(ctx, &actions, svc)
	return svc.Update(ctx)
}
