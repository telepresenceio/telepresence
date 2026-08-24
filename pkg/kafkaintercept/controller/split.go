package controller

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	api "github.com/telepresenceio/telepresence/v2/pkg/kafkaintercept/api/v1alpha1"
	"github.com/telepresenceio/telepresence/v2/pkg/kafkaintercept/broker"
)

func (r *SplitReconciler) reconcileEnabled(
	ctx context.Context,
	split *api.KafkaSplit,
	before *api.KafkaSplitStatus,
) (ctrl.Result, error) {
	if split.Status.ActiveGeneration != 0 && split.Status.ActiveGeneration != split.Generation {
		return r.reconcileDeactivation(ctx, split, before, false)
	}
	if split.Status.ActiveSpec == nil {
		workloads, err := r.resolveWorkloads(ctx, split)
		if err != nil {
			return r.pendingSplit(ctx, split, before, "WorkloadResolutionFailed", err.Error())
		}
		if len(workloads) == 0 {
			return r.pendingSplit(ctx, split, before, "NoWorkloads", "workload selector matched no supported workload")
		}
		if _, err := r.validateWorkloadEnvironment(ctx, split, workloads); err != nil {
			return r.pendingSplit(ctx, split, before, "InvalidWorkload", err.Error())
		}
		split.Status.ActiveGeneration = split.Generation
		split.Status.ActiveSpec = activeSpec(split)
		split.Status.Workloads = workloads
		split.Status.AdmissionMode = api.KafkaAdmissionNormal
		return r.transitionSplit(ctx, split, before, api.SplitPhasePreparing, "Preparing", "active workload snapshot recorded", true)
	}
	active := activeSplit(split)
	desiredPods, err := r.validateWorkloadEnvironment(ctx, active, split.Status.Workloads)
	if err != nil {
		return r.pendingSplit(ctx, split, before, "InvalidWorkload", err.Error())
	}
	if err := r.acquireOwnership(ctx, active); err != nil {
		return r.pendingSplit(ctx, split, before, "OwnershipUnavailable", err.Error())
	}
	kafka, err := r.openBroker(ctx, active)
	if err != nil {
		return r.pendingSplit(ctx, split, before, "BrokerUnavailable", err.Error())
	}
	defer kafka.Close()
	prepared, err := kafka.Prepare(ctx, active)
	if err != nil {
		return r.pendingSplit(ctx, split, before, "BrokerPreflightFailed", err.Error())
	}
	if err := verifySourceIncarnations(split.Status.SourceTopics, prepared.SourceTopics); err != nil {
		return r.transitionSplit(ctx, split, before, api.SplitPhaseDegraded, "SourceRecreated", err.Error(), true)
	}
	if err := verifyResourceIncarnations(split.Status.Resources, prepared.Resources); err != nil {
		return r.transitionSplit(ctx, split, before, api.SplitPhaseDegraded, "ShadowRecreated", err.Error(), true)
	}
	split.Status.SourceTopics = prepared.SourceTopics
	split.Status.ApplicationTopics = maps.Clone(prepared.ApplicationTopics)
	split.Status.ApplicationGroup = prepared.ApplicationGroup
	split.Status.Resources = slices.Clone(prepared.Resources)
	split.Status.ApplicationEnv = applicationEnvironment(
		active.Spec.Source, active.Spec.Application, prepared.ApplicationTopics, prepared.ApplicationGroup,
	)
	split.Status.TransactionalID = prepared.Names.ApplicationTransactional()
	if split.Status.AdmissionMode != api.KafkaAdmissionShadow {
		split.Status.AdmissionMode = api.KafkaAdmissionShadow
		return r.transitionSplit(
			ctx, split, before, api.SplitPhaseRedirecting, "Redirecting", "replacement Pods will consume application shadows", true,
		)
	}

	redirected, message, err := r.replacePods(ctx, active, desiredPods, split.Status.ActiveGeneration)
	if err != nil {
		return ctrl.Result{}, err
	}
	if !redirected {
		if split.Status.Phase.AcceptsRoutes() {
			return requeue(), nil
		}
		return r.pendingSplit(ctx, split, before, "ReplacingPods", message)
	}
	if split.Status.SplitterName == "" {
		memberless, err := kafka.GroupMemberless(ctx, active.Spec.Source.Group)
		if err != nil {
			return r.pendingSplit(ctx, split, before, "SourceOwnershipUnknown", err.Error())
		}
		if !memberless {
			return r.pendingSplit(ctx, split, before, "SourceGroupNotEmpty", "original application group still has consumers")
		}
	}
	table, err := r.desiredRoutingTable(ctx, active, false)
	if err != nil {
		return ctrl.Result{}, err
	}
	generation, err := r.ensureProviderResources(ctx, active, prepared, table)
	if err != nil {
		return r.pendingSplit(ctx, split, before, "SplitterUnavailable", err.Error())
	}
	split.Status.SplitterName = prepared.Names.KubernetesName
	split.Status.RouteGeneration = int64(generation)
	acknowledged, err := r.membersAcknowledged(
		ctx, split, prepared.Names.KubernetesName, generation, active.Spec.Splitter.EffectiveReplicas(),
	)
	if err != nil {
		return ctrl.Result{}, err
	}
	if !acknowledged {
		return r.transitionSplit(
			ctx, split, before, api.SplitPhaseStarting, "Starting", "waiting for splitter generation acknowledgements", true,
		)
	}
	members, err := kafka.GroupMembers(ctx, active.Spec.Source.Group)
	if err != nil {
		return r.pendingSplit(ctx, split, before, "SourceOwnershipUnknown", err.Error())
	}
	if err := verifySplitterMembers(members, prepared.Names.SplitterInstance, active.Spec.Splitter.EffectiveReplicas()); err != nil {
		return r.transitionSplit(ctx, split, before, api.SplitPhaseDegraded, "UnexpectedSourceMember", err.Error(), true)
	}
	return r.transitionSplit(
		ctx, split, before, api.SplitPhaseEnabled, "Enabled", "all source partitions have transactional splitter owners", true,
	)
}

func (r *SplitReconciler) reconcileSplitDeletion(
	ctx context.Context,
	split *api.KafkaSplit,
	before *api.KafkaSplitStatus,
) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(split, splitFinalizer) {
		return ctrl.Result{}, nil
	}
	return r.reconcileDeactivation(ctx, split, before, true)
}

func (r *SplitReconciler) reconcileDeactivation(
	ctx context.Context,
	split *api.KafkaSplit,
	before *api.KafkaSplitStatus,
	deleting bool,
) (ctrl.Result, error) {
	if split.Status.ActiveSpec == nil {
		if deleting {
			controllerutil.RemoveFinalizer(split, splitFinalizer)
			return ctrl.Result{}, r.Update(ctx, split)
		}
		split.Status.AdmissionMode = api.KafkaAdmissionNormal
		return r.transitionSplit(ctx, split, before, api.SplitPhaseDisabled, "Disabled", "Kafka split is disabled", false)
	}
	active := activeSplit(split)
	if split.Status.Phase == api.SplitPhaseCleaningApplication {
		return r.finishDeactivation(ctx, split, before, active, deleting)
	}
	if split.Status.Phase == api.SplitPhaseRestoringApplication {
		return r.reconcileRestoringApplication(ctx, split, before, active)
	}
	routes, err := r.closeRoutes(ctx, active)
	if err != nil {
		return ctrl.Result{}, err
	}
	prepared := preparedFromStatus(active)
	if split.Status.SplitterName != "" && split.Status.Phase != api.SplitPhaseStoppingSplitter {
		table, err := r.desiredRoutingTable(ctx, active, true)
		if err != nil {
			return ctrl.Result{}, err
		}
		generation, err := r.ensureProviderResources(ctx, active, prepared, table)
		if err != nil {
			return r.pendingSplit(ctx, split, before, "PauseFailed", err.Error())
		}
		split.Status.RouteGeneration = int64(generation)
		acknowledged, err := r.membersAcknowledged(
			ctx, split, split.Status.SplitterName, generation, active.Spec.Splitter.EffectiveReplicas(),
		)
		if err != nil {
			return ctrl.Result{}, err
		}
		if !acknowledged {
			return r.transitionSplit(
				ctx, split, before, api.SplitPhasePausing, "Pausing", "waiting for splitters to pause between transactions", true,
			)
		}
	}
	if routes > 0 {
		return r.transitionSplit(
			ctx, split, before, api.SplitPhaseClosingRoutes, "ClosingRoutes",
			fmt.Sprintf("waiting for %d Kafka routes to drain", routes), true,
		)
	}
	kafka, err := r.openBroker(ctx, active)
	if err != nil {
		return r.pendingSplit(ctx, split, before, "BrokerUnavailable", err.Error())
	}
	defer kafka.Close()
	remaining, err := kafka.Remaining(ctx, split.Status.ApplicationGroup, split.Status.ApplicationTopics)
	if err != nil {
		return r.pendingSplit(ctx, split, before, "ApplicationDrainUnknown", err.Error())
	}
	split.Status.ApplicationLag = &remaining
	if remaining > 0 {
		return r.transitionSplit(
			ctx, split, before, api.SplitPhaseDrainingApplication, "DrainingApplication",
			fmt.Sprintf("waiting for application to consume %d shadow records", remaining), true,
		)
	}
	if split.Status.AdmissionMode != api.KafkaAdmissionBlocked {
		split.Status.AdmissionMode = api.KafkaAdmissionBlocked
		return r.transitionSplit(
			ctx, split, before, api.SplitPhaseQuiescingApplication, "QuiescingApplication",
			"blocking replacements before source handback", true,
		)
	}
	removed, message, err := r.removePods(ctx, active)
	if err != nil {
		return ctrl.Result{}, err
	}
	if !removed {
		return r.pendingSplit(ctx, split, before, "QuiescingApplication", message)
	}
	memberless, err := kafka.GroupMemberless(ctx, split.Status.ApplicationGroup)
	if err != nil {
		return r.pendingSplit(ctx, split, before, "ApplicationOwnershipUnknown", err.Error())
	}
	if !memberless {
		return r.pendingSplit(ctx, split, before, "ApplicationGroupNotEmpty", "application shadow group still has consumers")
	}
	deleted, err := r.deleteProviderResources(ctx, split)
	if err != nil {
		return ctrl.Result{}, err
	}
	if !deleted {
		return r.transitionSplit(
			ctx, split, before, api.SplitPhaseStoppingSplitter, "StoppingSplitter",
			"waiting for splitters to leave the original group", true,
		)
	}
	split.Status.SplitterName = ""
	split.Status.Members = nil
	split.Status.RouteGeneration = 0
	memberless, err = kafka.GroupMemberless(ctx, active.Spec.Source.Group)
	if err != nil {
		return r.pendingSplit(ctx, split, before, "SourceOwnershipUnknown", err.Error())
	}
	if !memberless {
		return r.pendingSplit(ctx, split, before, "StoppingSplitter", "splitters have not left the original group")
	}
	if split.Status.AdmissionMode != api.KafkaAdmissionNormal {
		split.Status.AdmissionMode = api.KafkaAdmissionNormal
		return r.transitionSplit(
			ctx, split, before, api.SplitPhaseRestoringApplication, "RestoringApplication", "normally configured Pods may resume", true,
		)
	}
	return r.transitionSplit(
		ctx, split, before, api.SplitPhaseCleaningApplication, "CleaningApplication", "deleting managed application shadows", true,
	)
}

func (r *SplitReconciler) reconcileRestoringApplication(
	ctx context.Context,
	split *api.KafkaSplit,
	before *api.KafkaSplitStatus,
	active *api.KafkaSplit,
) (ctrl.Result, error) {
	desiredPods, err := r.validateWorkloadEnvironment(ctx, active, split.Status.Workloads)
	if err != nil {
		return r.pendingSplit(ctx, split, before, "InvalidWorkload", err.Error())
	}
	restored, message, err := r.replacePods(ctx, active, desiredPods, 0)
	if err != nil {
		return ctrl.Result{}, err
	}
	if !restored {
		return r.transitionSplit(ctx, split, before, split.Status.Phase, "RestoringApplication", message, true)
	}
	return r.transitionSplit(
		ctx, split, before, api.SplitPhaseCleaningApplication, "CleaningApplication", "deleting managed application shadows", true,
	)
}

func (r *SplitReconciler) finishDeactivation(
	ctx context.Context,
	split *api.KafkaSplit,
	before *api.KafkaSplitStatus,
	active *api.KafkaSplit,
	deleting bool,
) (ctrl.Result, error) {
	kafka, err := r.openBroker(ctx, active)
	if err != nil {
		return r.pendingSplit(ctx, split, before, "BrokerUnavailable", err.Error())
	}
	defer kafka.Close()
	if err := kafka.DeleteManaged(ctx, applicationResources(split.Status.Resources)); err != nil {
		return r.transitionSplit(ctx, split, before, split.Status.Phase, "CleanupPending", err.Error(), true)
	}
	if err := r.releaseOwnership(ctx, active); err != nil {
		return ctrl.Result{}, err
	}
	clearActiveStatus(&split.Status)
	if deleting {
		controllerutil.RemoveFinalizer(split, splitFinalizer)
		return ctrl.Result{}, r.Update(ctx, split)
	}
	if split.Spec.DesiredState == api.DesiredStateEnabled {
		return r.transitionSplit(
			ctx, split, before, api.SplitPhasePreparing, "SpecChanged",
			"previous active snapshot is disabled; preparing the new generation", true,
		)
	}
	return r.transitionSplit(ctx, split, before, api.SplitPhaseDisabled, "Disabled", "Kafka split is disabled", false)
}

func (r *SplitReconciler) closeRoutes(ctx context.Context, split *api.KafkaSplit) (int, error) {
	routes := new(api.KafkaRouteList)
	if err := r.List(
		ctx, routes, client.InNamespace(split.Namespace), client.MatchingFields{routeSplitRefIndexField: split.Name},
	); err != nil {
		return 0, err
	}
	remaining := 0
	for i := range routes.Items {
		route := &routes.Items[i]
		if route.Status.Phase == api.RoutePhaseClosed {
			continue
		}
		remaining++
		if route.Spec.DesiredState != api.RouteStateClosing {
			route.Spec.DesiredState = api.RouteStateClosing
			if err := r.Update(ctx, route); err != nil {
				return 0, err
			}
		}
	}
	return remaining, nil
}

func preparedFromStatus(split *api.KafkaSplit) broker.Prepared {
	names := broker.ResourceNames(split, split.Spec.Source.Group)
	return broker.Prepared{
		Names: names, SourceTopics: slices.Clone(split.Status.SourceTopics),
		ApplicationTopics: maps.Clone(split.Status.ApplicationTopics), ApplicationGroup: split.Status.ApplicationGroup,
		Resources: slices.Clone(split.Status.Resources),
	}
}

func applicationResources(resources []api.KafkaResourceStatus) []api.KafkaResourceStatus {
	return slices.DeleteFunc(slices.Clone(resources), func(resource api.KafkaResourceStatus) bool {
		return resource.RouteName != ""
	})
}

func clearActiveStatus(status *api.KafkaSplitStatus) {
	*status = api.KafkaSplitStatus{
		ObservedGeneration: status.ObservedGeneration,
		Phase:              status.Phase,
		Conditions:         status.Conditions,
		AdmissionMode:      api.KafkaAdmissionNormal,
	}
}

func verifySourceIncarnations(previous, current []api.KafkaTopicStatus) error {
	if len(previous) == 0 {
		return nil
	}
	byName := make(map[string]api.KafkaTopicStatus, len(current))
	for _, topic := range current {
		byName[topic.Name] = topic
	}
	for _, old := range previous {
		newTopic, ok := byName[old.Name]
		if !ok || old.TopicID != newTopic.TopicID {
			return fmt.Errorf("kafka source topic %s was recreated", old.Name)
		}
	}
	return nil
}

func verifyResourceIncarnations(previous, current []api.KafkaResourceStatus) error {
	byName := make(map[string]api.KafkaResourceStatus, len(current))
	for _, resource := range current {
		byName[resource.Name] = resource
	}
	for _, old := range previous {
		if old.TopicID == "" {
			continue
		}
		current, ok := byName[old.Name]
		if !ok || current.TopicID != old.TopicID {
			return fmt.Errorf("kafka shadow topic %s was recreated", old.Name)
		}
	}
	return nil
}

func verifySplitterMembers(members []string, prefix string, replicas int32) error {
	if len(members) != int(replicas) {
		return fmt.Errorf("original group has %d members, expected %d splitters", len(members), replicas)
	}
	for _, member := range members {
		if !strings.HasPrefix(member, prefix) {
			return fmt.Errorf("unexpected original-group member %s", member)
		}
	}
	return nil
}

func (r *SplitReconciler) pendingSplit(
	ctx context.Context,
	split *api.KafkaSplit,
	before *api.KafkaSplitStatus,
	reason, message string,
) (ctrl.Result, error) {
	phase := split.Status.Phase
	if phase != api.SplitPhaseDegraded {
		phase = api.SplitPhasePending
	}
	return r.transitionSplit(ctx, split, before, phase, reason, message, true)
}
