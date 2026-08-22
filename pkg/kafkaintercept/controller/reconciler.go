// Package controller contains the KafkaSplit and KafkaRoute Kubernetes
// reconcilers.
package controller

import (
	"context"
	"fmt"
	"reflect"
	"sort"
	"time"

	argorollouts "github.com/datawire/argo-rollouts-go-client/pkg/apis/rollouts/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apiMeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	api "github.com/telepresenceio/telepresence/v2/pkg/kafkaintercept/api/v1alpha1"
)

const validationRequeue = 30 * time.Second

// SplitReconciler validates desired state and publishes an immutable workload
// snapshot before any broker lifecycle begins.
type SplitReconciler struct {
	client.Client
}

// SetupWithManager registers the KafkaSplit controller.
func (r *SplitReconciler) SetupWithManager(manager ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(manager).For(&api.KafkaSplit{}).Complete(r)
}

// Reconcile validates one split and resolves its selected workloads.
func (r *SplitReconciler) Reconcile(ctx context.Context, request ctrl.Request) (ctrl.Result, error) {
	split := new(api.KafkaSplit)
	if err := r.Get(ctx, request.NamespacedName, split); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	before := split.Status.DeepCopy()
	split.Status.ObservedGeneration = split.Generation
	if err := split.Validate(); err != nil {
		split.Status.Phase = "Invalid"
		setCondition(&split.Status.Conditions, "Ready", metav1.ConditionFalse, "InvalidSpec", err.Error(), split.Generation)
		return r.updateSplitStatus(ctx, split, before)
	}
	if split.Spec.DesiredState == api.DesiredStateDisabled {
		split.Status.Phase = "Disabled"
		split.Status.Workloads = nil
		setCondition(&split.Status.Conditions, "Ready", metav1.ConditionFalse, "Disabled", "Kafka split is disabled", split.Generation)
		return r.updateSplitStatus(ctx, split, before)
	}

	workloads, err := r.resolveWorkloads(ctx, split)
	if err != nil {
		split.Status.Phase = "Pending"
		setCondition(&split.Status.Conditions, "Ready", metav1.ConditionFalse, "WorkloadResolutionFailed", err.Error(), split.Generation)
		result, updateErr := r.updateSplitStatus(ctx, split, before)
		if updateErr != nil {
			return result, updateErr
		}
		return ctrl.Result{RequeueAfter: validationRequeue}, nil
	}
	if len(workloads) == 0 {
		split.Status.Phase = "Pending"
		setCondition(&split.Status.Conditions, "Ready", metav1.ConditionFalse, "NoWorkloads", "workloadSelector matched no supported workload", split.Generation)
		result, updateErr := r.updateSplitStatus(ctx, split, before)
		if updateErr != nil {
			return result, updateErr
		}
		return ctrl.Result{RequeueAfter: validationRequeue}, nil
	}
	split.Status.Workloads = workloads
	split.Status.Phase = "Preparing"
	setCondition(
		&split.Status.Conditions, "Ready", metav1.ConditionFalse, "Preparing",
		"workload snapshot is valid; broker preflight and cutover are pending", split.Generation,
	)
	return r.updateSplitStatus(ctx, split, before)
}

func (r *SplitReconciler) updateSplitStatus(
	ctx context.Context,
	split *api.KafkaSplit,
	before *api.KafkaSplitStatus,
) (ctrl.Result, error) {
	if reflect.DeepEqual(*before, split.Status) {
		return ctrl.Result{}, nil
	}
	return ctrl.Result{}, r.Status().Update(ctx, split)
}

func (r *SplitReconciler) resolveWorkloads(ctx context.Context, split *api.KafkaSplit) ([]api.WorkloadReference, error) {
	selector, err := metav1.LabelSelectorAsSelector(&split.Spec.WorkloadSelector)
	if err != nil {
		return nil, err
	}
	options := []client.ListOption{client.InNamespace(split.Namespace), client.MatchingLabelsSelector{Selector: selector}}
	var refs []api.WorkloadReference

	deployments := new(appsv1.DeploymentList)
	if err := r.List(ctx, deployments, options...); err != nil {
		return nil, fmt.Errorf("list Deployments: %w", err)
	}
	for i := range deployments.Items {
		refs = append(refs, workloadReference("apps/v1", "Deployment", &deployments.Items[i]))
	}

	statefulSets := new(appsv1.StatefulSetList)
	if err := r.List(ctx, statefulSets, options...); err != nil {
		return nil, fmt.Errorf("list StatefulSets: %w", err)
	}
	for i := range statefulSets.Items {
		refs = append(refs, workloadReference("apps/v1", "StatefulSet", &statefulSets.Items[i]))
	}

	replicaSets := new(appsv1.ReplicaSetList)
	if err := r.List(ctx, replicaSets, options...); err != nil {
		return nil, fmt.Errorf("list ReplicaSets: %w", err)
	}
	for i := range replicaSets.Items {
		if metav1.GetControllerOf(&replicaSets.Items[i]) == nil {
			refs = append(refs, workloadReference("apps/v1", "ReplicaSet", &replicaSets.Items[i]))
		}
	}

	rollouts := new(argorollouts.RolloutList)
	if err := r.List(ctx, rollouts, options...); err != nil && !apiMeta.IsNoMatchError(err) && !apierrors.IsNotFound(err) {
		return nil, fmt.Errorf("list Argo Rollouts: %w", err)
	}
	for i := range rollouts.Items {
		refs = append(refs, workloadReference("argoproj.io/v1alpha1", "Rollout", &rollouts.Items[i]))
	}

	sort.Slice(refs, func(i, j int) bool {
		if refs[i].Kind != refs[j].Kind {
			return refs[i].Kind < refs[j].Kind
		}
		return refs[i].Name < refs[j].Name
	})
	return refs, nil
}

func workloadReference(apiVersion, kind string, object client.Object) api.WorkloadReference {
	return api.WorkloadReference{
		APIVersion: apiVersion, Kind: kind, Name: object.GetName(), UID: object.GetUID(),
	}
}

// RouteReconciler validates durable route ownership and expiry.
type RouteReconciler struct {
	client.Client
}

// SetupWithManager registers the KafkaRoute controller.
func (r *RouteReconciler) SetupWithManager(manager ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(manager).For(&api.KafkaRoute{}).Complete(r)
}

// Reconcile validates one route against its split.
func (r *RouteReconciler) Reconcile(ctx context.Context, request ctrl.Request) (ctrl.Result, error) {
	route := new(api.KafkaRoute)
	if err := r.Get(ctx, request.NamespacedName, route); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	before := route.Status.DeepCopy()
	route.Status.ObservedGeneration = route.Generation
	if err := route.Validate(); err != nil {
		route.Status.Phase = "Invalid"
		setCondition(&route.Status.Conditions, "Ready", metav1.ConditionFalse, "InvalidSpec", err.Error(), route.Generation)
		return r.updateRouteStatus(ctx, route, before)
	}
	split := new(api.KafkaSplit)
	if err := r.Get(ctx, client.ObjectKey{Namespace: route.Namespace, Name: route.Spec.SplitRef.Name}, split); err != nil {
		route.Status.Phase = "Pending"
		setCondition(&route.Status.Conditions, "Ready", metav1.ConditionFalse, "SplitUnavailable", err.Error(), route.Generation)
		result, updateErr := r.updateRouteStatus(ctx, route, before)
		if updateErr != nil {
			return result, updateErr
		}
		return ctrl.Result{RequeueAfter: validationRequeue}, nil
	}
	if !route.Spec.ExpiresAt.After(time.Now()) || route.Spec.DesiredState == api.RouteStateClosing {
		route.Status.Phase = "Closing"
		setCondition(&route.Status.Conditions, "Ready", metav1.ConditionFalse, "Closing", "route is closing", route.Generation)
	} else if split.Status.Phase != "Enabled" {
		route.Status.Phase = "Pending"
		setCondition(&route.Status.Conditions, "Ready", metav1.ConditionFalse, "SplitNotReady", "referenced KafkaSplit is not enabled", route.Generation)
	} else {
		route.Status.Phase = "Preparing"
		setCondition(&route.Status.Conditions, "Ready", metav1.ConditionFalse, "Preparing", "session resources are pending", route.Generation)
	}
	return r.updateRouteStatus(ctx, route, before)
}

func (r *RouteReconciler) updateRouteStatus(
	ctx context.Context,
	route *api.KafkaRoute,
	before *api.KafkaRouteStatus,
) (ctrl.Result, error) {
	if reflect.DeepEqual(*before, route.Status) {
		return ctrl.Result{}, nil
	}
	return ctrl.Result{}, r.Status().Update(ctx, route)
}

func setCondition(
	conditions *[]metav1.Condition,
	typeName string,
	status metav1.ConditionStatus,
	reason, message string,
	generation int64,
) {
	apiMeta.SetStatusCondition(conditions, metav1.Condition{
		Type: typeName, Status: status, Reason: reason, Message: message, ObservedGeneration: generation,
	})
}
