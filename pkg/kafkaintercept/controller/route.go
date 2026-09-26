package controller

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/telepresenceio/telepresence/v2/pkg/kafkaintercept"
	api "github.com/telepresenceio/telepresence/v2/pkg/kafkaintercept/api/v1alpha1"
	"github.com/telepresenceio/telepresence/v2/pkg/kafkaintercept/broker"
	"github.com/telepresenceio/telepresence/v2/pkg/kafkaintercept/runtimeconfig"
)

func (r *RouteReconciler) reconcileRoute(
	ctx context.Context,
	route *api.KafkaRoute,
	before *api.KafkaRouteStatus,
) (ctrl.Result, error) {
	if route.Status.Phase == api.RoutePhaseClosed {
		if controllerutil.ContainsFinalizer(route, routeFinalizer) {
			controllerutil.RemoveFinalizer(route, routeFinalizer)
			return requeue(), r.Update(ctx, route)
		}
		if route.DeletionTimestamp == nil && !route.Spec.ExpiresAt.After(time.Now()) {
			return ctrl.Result{}, r.Delete(ctx, route)
		}
		return ctrl.Result{}, nil
	}
	split := new(api.KafkaSplit)
	if err := r.Get(ctx, client.ObjectKey{Namespace: route.Namespace, Name: route.Spec.SplitRef.Name}, split); err != nil {
		if apierrors.IsNotFound(err) && route.DeletionTimestamp != nil {
			return r.pendingRoute(ctx, route, before, "SplitMissing", "referenced KafkaSplit disappeared before route cleanup")
		}
		return r.pendingRoute(ctx, route, before, "SplitUnavailable", err.Error())
	}
	closing := route.DeletionTimestamp != nil || route.Spec.DesiredState == api.RouteStateClosing ||
		!route.Spec.ExpiresAt.After(time.Now()) || split.Spec.DesiredState == api.DesiredStateDisabled ||
		split.Status.ActiveGeneration != split.Generation
	if closing {
		return r.reconcileClosingRoute(ctx, route, before, split)
	}
	if !split.Status.Phase.AcceptsRoutes() {
		return r.pendingRoute(ctx, route, before, "SplitNotReady", "referenced KafkaSplit is not enabled")
	}
	if err := r.rejectOverlap(ctx, route); err != nil {
		return r.transitionRoute(ctx, route, before, api.RoutePhaseInvalid, "OverlappingPredicate", err.Error(), false)
	}
	active := activeSplit(split)
	kafka, err := r.openBroker(ctx, active)
	if err != nil {
		return r.pendingRoute(ctx, route, before, "BrokerUnavailable", err.Error())
	}
	defer kafka.Close()
	brokerRoute, err := r.routeForSessionSlot(ctx, active, route)
	if err != nil {
		return r.pendingRoute(ctx, route, before, "NeedsProvisioning", err.Error())
	}
	session, err := kafka.EnsureSession(ctx, active, brokerRoute)
	if err != nil {
		return r.pendingRoute(ctx, route, before, "SessionPreparationFailed", err.Error())
	}
	if err := verifyResourceIncarnations(route.Status.Resources, session.Resources); err != nil {
		return r.pendingRoute(ctx, route, before, "ShadowRecreated", err.Error())
	}
	for i := range session.Resources {
		session.Resources[i].RouteName = route.Name
	}
	route.Status.Group = session.Group
	route.Status.Topics = maps.Clone(session.Topics)
	route.Status.Resources = slices.Clone(session.Resources)
	route.Status.Environment = applicationEnvironment(
		active.Spec.Source, active.Spec.Application, session.Topics, session.Group,
	)
	if name := active.Spec.Application.TransactionalIDEnv; name != "" {
		route.Status.Environment[name] = broker.ResourceNames(active, active.Spec.Source.Group).ClientTransactional(route.Name)
	}
	generation, included, err := r.routeGeneration(ctx, split, route.Name)
	if err != nil {
		return ctrl.Result{}, err
	}
	if !included {
		return r.transitionRoute(ctx, route, before, api.RoutePhaseStaged, "Staged", "waiting for the split routing table", true)
	}
	route.Status.RouteGeneration = int64(generation)
	if !membersHaveGeneration(split.Status.Members, generation) {
		return r.transitionRoute(
			ctx, route, before, api.RoutePhaseStaged, "AwaitingAcknowledgement", "waiting for every splitter to adopt the route", true,
		)
	}
	return r.transitionRoute(ctx, route, before, api.RoutePhaseReady, "Ready", "all splitters acknowledged the personal route", true)
}

func (r *RouteReconciler) reconcileClosingRoute(
	ctx context.Context,
	route *api.KafkaRoute,
	before *api.KafkaRouteStatus,
	split *api.KafkaSplit,
) (ctrl.Result, error) {
	if len(route.Status.Resources) == 0 {
		route.Status.Environment = nil
		route.Status.Topics = nil
		route.Status.Group = ""
		return r.transitionRoute(ctx, route, before, api.RoutePhaseClosed, "Closed", "Kafka route is closed", true)
	}
	if route.Status.Phase == api.RoutePhaseCleaning {
		return r.finishClosingRoute(ctx, route, before, split)
	}
	generation, included, err := r.routeGeneration(ctx, split, route.Name)
	if err != nil {
		return ctrl.Result{}, err
	}
	if included || !membersHaveGeneration(split.Status.Members, generation) {
		return r.transitionRoute(ctx, route, before, api.RoutePhaseClosing, "Closing", "waiting for splitters to stop routing new records", true)
	}
	active := activeSplit(split)
	kafka, err := r.openBroker(ctx, active)
	if err != nil {
		return r.pendingRoute(ctx, route, before, "BrokerUnavailable", err.Error())
	}
	defer kafka.Close()
	if reason, err := drainClosingRoute(ctx, kafka, active, route, split.Status.ApplicationTopics); err != nil {
		return r.pendingRoute(ctx, route, before, reason, err.Error())
	}
	return r.transitionRoute(ctx, route, before, api.RoutePhaseCleaning, "Cleaning", "deleting managed session resources", true)
}

func (r *RouteReconciler) finishClosingRoute(
	ctx context.Context,
	route *api.KafkaRoute,
	before *api.KafkaRouteStatus,
	split *api.KafkaSplit,
) (ctrl.Result, error) {
	active := activeSplit(split)
	generation, included, err := r.routeGeneration(ctx, split, route.Name)
	if err != nil {
		return r.pendingRoute(ctx, route, before, "RoutingUnknown", err.Error())
	}
	replicas := active.Spec.Splitter.EffectiveReplicas()
	members, err := splitterMemberStatus(ctx, r.Client, r.providerNamespace(), split.Status.SplitterName, replicas)
	if err != nil {
		return r.pendingRoute(ctx, route, before, "AcknowledgementUnknown", err.Error())
	}
	if included || !membersReady(members, replicas, generation) {
		return r.pendingRoute(ctx, route, before, "AwaitingAcknowledgement", "waiting for every splitter to confirm the closing route generation")
	}
	kafka, err := r.openBroker(ctx, active)
	if err != nil {
		return r.transitionRoute(ctx, route, before, route.Status.Phase, "BrokerUnavailable", err.Error(), true)
	}
	defer kafka.Close()
	if reason, err := drainClosingRoute(ctx, kafka, active, route, split.Status.ApplicationTopics); err != nil {
		return r.pendingRoute(ctx, route, before, reason, err.Error())
	}
	if err := kafka.DeleteManaged(ctx, route.Status.Resources); err != nil {
		return r.transitionRoute(ctx, route, before, route.Status.Phase, "CleanupPending", err.Error(), true)
	}
	route.Status.Group = ""
	route.Status.Topics = nil
	route.Status.Environment = nil
	route.Status.Resources = nil
	route.Status.RouteGeneration = 0
	route.Status.Lag = nil
	return r.transitionRoute(ctx, route, before, api.RoutePhaseClosed, "Closed", "session residue returned to the application shadow", true)
}

func drainClosingRoute(
	ctx context.Context,
	kafka kafkaBroker,
	split *api.KafkaSplit,
	route *api.KafkaRoute,
	applicationTopics map[string]string,
) (string, error) {
	memberless, err := kafka.GroupMemberless(ctx, route.Status.Group)
	if err != nil {
		return "SessionOwnershipUnknown", err
	}
	if !memberless {
		return "SessionGroupNotEmpty", errors.New("local Kafka consumer group still has members")
	}
	if err := kafka.DrainSession(ctx, split, route.Name, route.Status.Group, route.Status.Topics, applicationTopics); err != nil {
		if errors.Is(err, broker.ErrSessionGroupNotEmpty) {
			return "SessionGroupNotEmpty", err
		}
		return "DrainFailed", err
	}
	// DrainSession only returns nil after its own residue check found zero records remaining.
	var zero int64
	route.Status.Lag = &zero
	return "", nil
}

func (r *RouteReconciler) routeForSessionSlot(
	ctx context.Context,
	split *api.KafkaSplit,
	route *api.KafkaRoute,
) (*api.KafkaRoute, error) {
	if split.Spec.Shadows.Mode != api.ShadowModePreprovisioned || route.Status.Group != "" {
		return route, nil
	}
	routes := new(api.KafkaRouteList)
	if err := r.List(
		ctx, routes, client.InNamespace(route.Namespace), client.MatchingFields{routeSplitRefIndexField: split.Name},
	); err != nil {
		return nil, err
	}
	used := make(map[string]struct{})
	for i := range routes.Items {
		if routes.Items[i].Name != route.Name {
			used[routes.Items[i].Status.Group] = struct{}{}
		}
	}
	for _, slot := range split.Spec.Shadows.Preprovisioned.Sessions {
		if _, ok := used[slot.Group]; ok {
			continue
		}
		clone := route.DeepCopy()
		clone.Name = slot.Name
		return clone, nil
	}
	return nil, fmt.Errorf("no free preprovisioned Kafka session slot")
}

func (r *RouteReconciler) rejectOverlap(ctx context.Context, route *api.KafkaRoute) error {
	routes := new(api.KafkaRouteList)
	if err := r.List(
		ctx, routes, client.InNamespace(route.Namespace), client.MatchingFields{routeSplitRefIndexField: route.Spec.SplitRef.Name},
	); err != nil {
		return err
	}
	predicate := predicateFromAPI(route.Spec.Predicate)
	for i := range routes.Items {
		other := &routes.Items[i]
		if other.Name == route.Name ||
			other.Spec.DesiredState != api.RouteStateActive || !other.Spec.ExpiresAt.After(time.Now()) ||
			other.Status.Phase == api.RoutePhaseClosed || other.Status.Phase == api.RoutePhaseInvalid {
			continue
		}
		if predicate.Overlaps(predicateFromAPI(other.Spec.Predicate)) {
			ours := route.CreationTimestamp.Time
			theirs := other.CreationTimestamp.Time
			if theirs.Before(ours) || theirs.Equal(ours) && other.Name < route.Name {
				return fmt.Errorf("KafkaRoute %s has an overlapping predicate", other.Name)
			}
		}
	}
	return nil
}

func (r *RouteReconciler) routeGeneration(
	ctx context.Context,
	split *api.KafkaSplit,
	route string,
) (uint64, bool, error) {
	if split.Status.SplitterName == "" {
		return 0, false, nil
	}
	configMap := new(corev1.ConfigMap)
	if err := r.Get(ctx, client.ObjectKey{Namespace: r.providerNamespace(), Name: split.Status.SplitterName}, configMap); err != nil {
		if apierrors.IsNotFound(err) {
			return 0, false, nil
		}
		return 0, false, err
	}
	table, err := runtimeconfig.RoutingFromConfigMap(configMap)
	if err != nil {
		return 0, false, err
	}
	return table.Generation, slices.ContainsFunc(table.Routes, func(candidate kafkaintercept.Route) bool {
		return candidate.ID == route
	}), nil
}

func membersHaveGeneration(members []api.KafkaSplitterMemberStatus, generation uint64) bool {
	return len(members) > 0 && !slices.ContainsFunc(members, func(member api.KafkaSplitterMemberStatus) bool {
		return !member.Healthy || member.Generation < int64(generation)
	})
}

func membersReady(members []api.KafkaSplitterMemberStatus, replicas int32, generation uint64) bool {
	return len(members) == int(replicas) && membersHaveGeneration(members, generation)
}

func (r *RouteReconciler) pendingRoute(
	ctx context.Context,
	route *api.KafkaRoute,
	before *api.KafkaRouteStatus,
	reason, message string,
) (ctrl.Result, error) {
	phase := route.Status.Phase
	if phase != api.RoutePhaseInvalid {
		phase = api.RoutePhasePending
	}
	return r.transitionRoute(ctx, route, before, phase, reason, message, true)
}
