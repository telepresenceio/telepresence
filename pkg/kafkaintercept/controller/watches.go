package controller

import (
	"context"

	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	api "github.com/telepresenceio/telepresence/v2/pkg/kafkaintercept/api/v1alpha1"
	"github.com/telepresenceio/telepresence/v2/pkg/kafkaintercept/runtimeconfig"
)

// routeToSplit maps a KafkaRoute to the split it references.
func routeToSplit(_ context.Context, obj client.Object) []reconcile.Request {
	route, ok := obj.(*api.KafkaRoute)
	if !ok {
		return nil
	}
	return []reconcile.Request{{NamespacedName: types.NamespacedName{
		Namespace: route.Namespace, Name: route.Spec.SplitRef.Name,
	}}}
}

// labelsToSplit maps an object carrying both split labels to that split.
func labelsToSplit(_ context.Context, obj client.Object) []reconcile.Request {
	labels := obj.GetLabels()
	namespace, name := labels[runtimeconfig.SplitNamespaceLabel], labels[runtimeconfig.SplitNameLabel]
	if namespace == "" || name == "" {
		return nil
	}
	return []reconcile.Request{{NamespacedName: types.NamespacedName{Namespace: namespace, Name: name}}}
}

// podToSplits maps a Pod to every KafkaSplit in its namespace that has an
// active workload snapshot.
func (r *SplitReconciler) podToSplits(ctx context.Context, obj client.Object) []reconcile.Request {
	pod, ok := obj.(*corev1.Pod)
	if !ok {
		return nil
	}
	splits := new(api.KafkaSplitList)
	if err := r.List(ctx, splits, client.InNamespace(pod.Namespace)); err != nil {
		return nil
	}
	var requests []reconcile.Request
	for i := range splits.Items {
		split := &splits.Items[i]
		if len(split.Status.Workloads) == 0 {
			continue
		}
		requests = append(requests, reconcile.Request{
			NamespacedName: types.NamespacedName{Namespace: split.Namespace, Name: split.Name},
		})
	}
	return requests
}

func hasSplitLabels(obj client.Object) bool {
	labels := obj.GetLabels()
	return labels[runtimeconfig.SplitNamespaceLabel] != "" && labels[runtimeconfig.SplitNameLabel] != ""
}

// memberLeaseChanged ignores a member Lease's renew-only updates, so a
// splitter's periodic heartbeat does not trigger a reconcile by itself.
func memberLeaseChanged() predicate.Funcs {
	return predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool { return hasSplitLabels(e.Object) },
		DeleteFunc: func(e event.DeleteEvent) bool { return hasSplitLabels(e.Object) },
		UpdateFunc: func(e event.UpdateEvent) bool {
			if !hasSplitLabels(e.ObjectNew) {
				return false
			}
			oldLease, ok := e.ObjectOld.(*coordinationv1.Lease)
			if !ok {
				return false
			}
			newLease, ok := e.ObjectNew.(*coordinationv1.Lease)
			if !ok {
				return false
			}
			return oldLease.Annotations[runtimeconfig.GenerationAnnotation] != newLease.Annotations[runtimeconfig.GenerationAnnotation] ||
				oldLease.Annotations[runtimeconfig.HealthyAnnotation] != newLease.Annotations[runtimeconfig.HealthyAnnotation]
		},
		GenericFunc: func(event.GenericEvent) bool { return false },
	}
}

// podChanged ignores a Pod update unless readiness, deletion, or the active
// generation annotation changed.
func podChanged() predicate.Funcs {
	return predicate.Funcs{
		CreateFunc: func(event.CreateEvent) bool { return true },
		DeleteFunc: func(event.DeleteEvent) bool { return true },
		UpdateFunc: func(e event.UpdateEvent) bool {
			oldPod, ok := e.ObjectOld.(*corev1.Pod)
			if !ok {
				return false
			}
			newPod, ok := e.ObjectNew.(*corev1.Pod)
			if !ok {
				return false
			}
			if podReady(oldPod) != podReady(newPod) {
				return true
			}
			if (oldPod.DeletionTimestamp != nil) != (newPod.DeletionTimestamp != nil) {
				return true
			}
			return oldPod.Annotations[runtimeconfig.ActiveAnnotation] != newPod.Annotations[runtimeconfig.ActiveAnnotation]
		},
		GenericFunc: func(event.GenericEvent) bool { return false },
	}
}
