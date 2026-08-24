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
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

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

// base holds the broker-opening behavior shared by both reconcilers.
type base struct {
	client.Client
	ProviderNamespace string
	OpenBroker        brokerOpener
}

func (b *base) openBroker(ctx context.Context, split *api.KafkaSplit) (kafkaBroker, error) {
	opener := b.OpenBroker
	if opener == nil {
		opener = defaultBrokerOpener
	}
	return opener(ctx, b.Client, split.Namespace, split.Spec.Connection)
}

func (b *base) providerNamespace() string {
	if b.ProviderNamespace != "" {
		return b.ProviderNamespace
	}
	return "ambassador"
}

// SplitReconciler owns the workload cutover and splitter lifecycle.
type SplitReconciler struct {
	base
	ProviderImage  string
	ServiceAccount string
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
		return r.transitionSplit(ctx, split, before, api.SplitPhaseInvalid, "InvalidSpec", err.Error(), false)
	}
	if !controllerutil.ContainsFinalizer(split, splitFinalizer) {
		controllerutil.AddFinalizer(split, splitFinalizer)
		if err := r.Update(ctx, split); err != nil {
			return ctrl.Result{}, err
		}
		return requeue(), nil
	}
	if split.Spec.DesiredState == api.DesiredStateDisabled {
		return r.reconcileDeactivation(ctx, split, before, false)
	}
	return r.reconcileEnabled(ctx, split, before)
}

func (r *SplitReconciler) updateSplitStatus(
	ctx context.Context,
	split *api.KafkaSplit,
	before *api.KafkaSplitStatus,
	requeueAfter bool,
) (ctrl.Result, error) {
	if !reflect.DeepEqual(*before, split.Status) {
		if err := r.Status().Update(ctx, split); err != nil {
			return ctrl.Result{}, err
		}
	}
	if requeueAfter {
		return requeue(), nil
	}
	return ctrl.Result{}, nil
}

// transitionSplit sets the split's phase and Ready condition, then persists the status.
func (r *SplitReconciler) transitionSplit(
	ctx context.Context,
	split *api.KafkaSplit,
	before *api.KafkaSplitStatus,
	phase api.SplitPhase,
	reason, message string,
	requeueAfter bool,
) (ctrl.Result, error) {
	split.Status.Phase = phase
	status := metav1.ConditionFalse
	if phase == api.SplitPhaseEnabled {
		status = metav1.ConditionTrue
	}
	setReadyCondition(&split.Status.Conditions, status, reason, message, split.Generation)
	return r.updateSplitStatus(ctx, split, before, requeueAfter)
}

// RouteReconciler owns one personal shadow and its transactional drain.
type RouteReconciler struct {
	base
}

// routeSplitRefIndexField indexes KafkaRoutes by their spec.splitRef.name.
const routeSplitRefIndexField = "spec.splitRef.name"

func routeSplitRefIndexer(obj client.Object) []string {
	route, ok := obj.(*api.KafkaRoute)
	if !ok {
		return nil
	}
	return []string{route.Spec.SplitRef.Name}
}

// SetupWithManager registers the KafkaRoute controller.
func (r *RouteReconciler) SetupWithManager(manager ctrl.Manager) error {
	if err := manager.GetFieldIndexer().IndexField(
		context.Background(), &api.KafkaRoute{}, routeSplitRefIndexField, routeSplitRefIndexer,
	); err != nil {
		return err
	}
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
		return r.transitionRoute(ctx, route, before, api.RoutePhaseInvalid, "InvalidSpec", err.Error(), false)
	}
	if route.Status.Phase != api.RoutePhaseClosed && route.DeletionTimestamp == nil &&
		!controllerutil.ContainsFinalizer(route, routeFinalizer) {
		controllerutil.AddFinalizer(route, routeFinalizer)
		if err := r.Update(ctx, route); err != nil {
			return ctrl.Result{}, err
		}
		return requeue(), nil
	}
	return r.reconcileRoute(ctx, route, before)
}

func (r *RouteReconciler) updateRouteStatus(
	ctx context.Context,
	route *api.KafkaRoute,
	before *api.KafkaRouteStatus,
	requeueAfter bool,
) (ctrl.Result, error) {
	if !reflect.DeepEqual(*before, route.Status) {
		if err := r.Status().Update(ctx, route); err != nil {
			return ctrl.Result{}, err
		}
	}
	if requeueAfter {
		return requeue(), nil
	}
	return ctrl.Result{}, nil
}

// transitionRoute sets the route's phase and Ready condition, then persists the status.
func (r *RouteReconciler) transitionRoute(
	ctx context.Context,
	route *api.KafkaRoute,
	before *api.KafkaRouteStatus,
	phase api.RoutePhase,
	reason, message string,
	requeueAfter bool,
) (ctrl.Result, error) {
	route.Status.Phase = phase
	status := metav1.ConditionFalse
	if phase == api.RoutePhaseReady {
		status = metav1.ConditionTrue
	}
	setReadyCondition(&route.Status.Conditions, status, reason, message, route.Generation)
	return r.updateRouteStatus(ctx, route, before, requeueAfter)
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
