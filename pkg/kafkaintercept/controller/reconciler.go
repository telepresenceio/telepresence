// Package controller reconciles Kafka personal intercept resources.
package controller

import (
	"context"
	"reflect"
	"time"

	apiMeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	api "github.com/telepresenceio/telepresence/v2/pkg/kafkaintercept/api/v1alpha1"
	"github.com/telepresenceio/telepresence/v2/pkg/kafkaintercept/broker"
	"github.com/telepresenceio/telepresence/v2/pkg/kafkaintercept/runtimeconfig"
)

const (
	reconcileInterval = 2 * time.Second
	splitFinalizer    = runtimeconfig.SplitFinalizer
	routeFinalizer    = runtimeconfig.RouteFinalizer
)

type kafkaBroker interface {
	Prepare(context.Context, *api.KafkaSplit) (broker.Prepared, error)
	EnsureSession(context.Context, *api.KafkaSplit, *api.KafkaRoute) (broker.Session, error)
	GroupMemberless(context.Context, string) (bool, error)
	GroupMembers(context.Context, string) ([]string, error)
	Remaining(context.Context, string, map[string]string) (int64, error)
	DrainSession(context.Context, *api.KafkaSplit, string, string, map[string]string, map[string]string) error
	DeleteManaged(context.Context, []api.KafkaResourceStatus) error
	Close()
}

type brokerOpener func(context.Context, client.Reader, string, api.KafkaConnectionSpec) (kafkaBroker, error)

func defaultBrokerOpener(
	ctx context.Context,
	reader client.Reader,
	namespace string,
	connection api.KafkaConnectionSpec,
) (kafkaBroker, error) {
	return broker.Open(ctx, reader, namespace, connection)
}

// SplitReconciler owns the workload cutover and splitter lifecycle.
type SplitReconciler struct {
	client.Client
	ProviderNamespace string
	ProviderImage     string
	ServiceAccount    string
	OpenBroker        brokerOpener
}

// SetupWithManager registers the KafkaSplit controller.
func (r *SplitReconciler) SetupWithManager(manager ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(manager).For(&api.KafkaSplit{}).Complete(r)
}

// Reconcile advances one split through its requested lifecycle.
func (r *SplitReconciler) Reconcile(ctx context.Context, request ctrl.Request) (ctrl.Result, error) {
	split := new(api.KafkaSplit)
	if err := r.Get(ctx, request.NamespacedName, split); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	before := split.Status.DeepCopy()
	split.Status.ObservedGeneration = split.Generation
	if split.DeletionTimestamp != nil {
		return r.reconcileSplitDeletion(ctx, split, before)
	}
	if split.Status.ActiveSpec != nil &&
		(split.Spec.DesiredState == api.DesiredStateDisabled || split.Status.ActiveGeneration != split.Generation) {
		return r.reconcileDeactivation(ctx, split, before, false)
	}
	if err := split.Validate(); err != nil {
		split.Status.Phase = "Invalid"
		setReadyCondition(&split.Status.Conditions, metav1.ConditionFalse, "InvalidSpec", err.Error(), split.Generation)
		return r.updateSplitStatus(ctx, split, before, false)
	}
	if !containsString(split.Finalizers, splitFinalizer) {
		split.Finalizers = append(split.Finalizers, splitFinalizer)
		if err := r.Update(ctx, split); err != nil {
			return ctrl.Result{}, err
		}
		return requeue(), nil
	}
	if split.Spec.DesiredState == api.DesiredStateDisabled {
		return r.reconcileDisabled(ctx, split, before)
	}
	return r.reconcileEnabled(ctx, split, before)
}

func (r *SplitReconciler) openBroker(ctx context.Context, split *api.KafkaSplit) (kafkaBroker, error) {
	opener := r.OpenBroker
	if opener == nil {
		opener = defaultBrokerOpener
	}
	return opener(ctx, r.Client, split.Namespace, split.Spec.Connection)
}

func (r *SplitReconciler) providerNamespace() string {
	if r.ProviderNamespace != "" {
		return r.ProviderNamespace
	}
	return "ambassador"
}

func (r *SplitReconciler) updateSplitStatus(
	ctx context.Context,
	split *api.KafkaSplit,
	before *api.KafkaSplitStatus,
	requeueAfter bool,
) (ctrl.Result, error) {
	if reflect.DeepEqual(*before, split.Status) {
		if requeueAfter {
			return requeue(), nil
		}
		return ctrl.Result{}, nil
	}
	if err := r.Status().Update(ctx, split); err != nil {
		return ctrl.Result{}, err
	}
	if requeueAfter {
		return requeue(), nil
	}
	return ctrl.Result{}, nil
}

// RouteReconciler owns one personal shadow and its transactional drain.
type RouteReconciler struct {
	client.Client
	ProviderNamespace string
	OpenBroker        brokerOpener
}

// SetupWithManager registers the KafkaRoute controller.
func (r *RouteReconciler) SetupWithManager(manager ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(manager).For(&api.KafkaRoute{}).Complete(r)
}

// Reconcile advances one personal route.
func (r *RouteReconciler) Reconcile(ctx context.Context, request ctrl.Request) (ctrl.Result, error) {
	route := new(api.KafkaRoute)
	if err := r.Get(ctx, request.NamespacedName, route); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	before := route.Status.DeepCopy()
	route.Status.ObservedGeneration = route.Generation
	if err := route.Validate(); err != nil {
		route.Status.Phase = "Invalid"
		setReadyCondition(&route.Status.Conditions, metav1.ConditionFalse, "InvalidSpec", err.Error(), route.Generation)
		return r.updateRouteStatus(ctx, route, before, false)
	}
	if route.Status.Phase != "Closed" && route.DeletionTimestamp == nil && !containsString(route.Finalizers, routeFinalizer) {
		route.Finalizers = append(route.Finalizers, routeFinalizer)
		if err := r.Update(ctx, route); err != nil {
			return ctrl.Result{}, err
		}
		return requeue(), nil
	}
	return r.reconcileRoute(ctx, route, before)
}

func (r *RouteReconciler) openBroker(ctx context.Context, split *api.KafkaSplit) (kafkaBroker, error) {
	opener := r.OpenBroker
	if opener == nil {
		opener = defaultBrokerOpener
	}
	return opener(ctx, r.Client, split.Namespace, split.Spec.Connection)
}

func (r *RouteReconciler) providerNamespace() string {
	if r.ProviderNamespace != "" {
		return r.ProviderNamespace
	}
	return "ambassador"
}

func (r *RouteReconciler) updateRouteStatus(
	ctx context.Context,
	route *api.KafkaRoute,
	before *api.KafkaRouteStatus,
	requeueAfter bool,
) (ctrl.Result, error) {
	if reflect.DeepEqual(*before, route.Status) {
		if requeueAfter {
			return requeue(), nil
		}
		return ctrl.Result{}, nil
	}
	if err := r.Status().Update(ctx, route); err != nil {
		return ctrl.Result{}, err
	}
	if requeueAfter {
		return requeue(), nil
	}
	return ctrl.Result{}, nil
}

func setReadyCondition(
	conditions *[]metav1.Condition,
	status metav1.ConditionStatus,
	reason, message string,
	generation int64,
) {
	apiMeta.SetStatusCondition(conditions, metav1.Condition{
		Type: "Ready", Status: status, Reason: reason, Message: message, ObservedGeneration: generation,
	})
}

func requeue() ctrl.Result {
	return ctrl.Result{RequeueAfter: reconcileInterval}
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func removeString(values []string, target string) []string {
	for i := range values {
		if values[i] == target {
			return append(values[:i], values[i+1:]...)
		}
	}
	return values
}
