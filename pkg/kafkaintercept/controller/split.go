package controller

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

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
		split.Status.Phase = "Preparing"
		setReadyCondition(&split.Status.Conditions, metav1.ConditionFalse, "Preparing", "active workload snapshot recorded", split.Generation)
		return r.updateSplitStatus(ctx, split, before, true)
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
		split.Status.Phase = "Degraded"
		setReadyCondition(&split.Status.Conditions, metav1.ConditionFalse, "SourceRecreated", err.Error(), split.Generation)
		return r.updateSplitStatus(ctx, split, before, true)
	}
	if err := verifyResourceIncarnations(split.Status.Resources, prepared.Resources); err != nil {
		split.Status.Phase = "Degraded"
		setReadyCondition(&split.Status.Conditions, metav1.ConditionFalse, "ShadowRecreated", err.Error(), split.Generation)
		return r.updateSplitStatus(ctx, split, before, true)
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
		split.Status.Phase = "Redirecting"
		setReadyCondition(&split.Status.Conditions, metav1.ConditionFalse, "Redirecting", "replacement Pods will consume application shadows", split.Generation)
		return r.updateSplitStatus(ctx, split, before, true)
	}

	redirected, message, err := r.replacePods(ctx, active, desiredPods, split.Status.ActiveGeneration)
	if err != nil {
		return ctrl.Result{}, err
	}
	if !redirected {
		if routeProvisioningAllowed(split.Status.Phase) {
			return requeue(), nil
		}
		return r.pendingSplit(ctx, split, before, "ReplacingPods", message)
	}
	memberless, err := kafka.GroupMemberless(ctx, active.Spec.Source.Group)
	if err != nil {
		return r.pendingSplit(ctx, split, before, "SourceOwnershipUnknown", err.Error())
	}
	if !memberless && split.Status.SplitterName == "" {
		return r.pendingSplit(ctx, split, before, "SourceGroupNotEmpty", "original application group still has consumers")
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
		ctx, split, prepared.Names.KubernetesName, generation, splitterReplicas(active.Spec.Splitter),
	)
	if err != nil {
		return ctrl.Result{}, err
	}
	if !acknowledged {
		split.Status.Phase = "Starting"
		setReadyCondition(&split.Status.Conditions, metav1.ConditionFalse, "Starting", "waiting for splitter generation acknowledgements", split.Generation)
		return r.updateSplitStatus(ctx, split, before, true)
	}
	members, err := kafka.GroupMembers(ctx, active.Spec.Source.Group)
	if err != nil {
		return r.pendingSplit(ctx, split, before, "SourceOwnershipUnknown", err.Error())
	}
	if err := verifySplitterMembers(members, prepared.Names.SplitterInstance, splitterReplicas(active.Spec.Splitter)); err != nil {
		split.Status.Phase = "Degraded"
		setReadyCondition(&split.Status.Conditions, metav1.ConditionFalse, "UnexpectedSourceMember", err.Error(), split.Generation)
		return r.updateSplitStatus(ctx, split, before, true)
	}
	split.Status.Phase = "Enabled"
	setReadyCondition(&split.Status.Conditions, metav1.ConditionTrue, "Enabled", "all source partitions have transactional splitter owners", split.Generation)
	return r.updateSplitStatus(ctx, split, before, true)
}

func (r *SplitReconciler) reconcileDisabled(
	ctx context.Context,
	split *api.KafkaSplit,
	before *api.KafkaSplitStatus,
) (ctrl.Result, error) {
	return r.reconcileDeactivation(ctx, split, before, false)
}

func (r *SplitReconciler) reconcileSplitDeletion(
	ctx context.Context,
	split *api.KafkaSplit,
	before *api.KafkaSplitStatus,
) (ctrl.Result, error) {
	if !containsString(split.Finalizers, splitFinalizer) {
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
			split.Finalizers = removeString(split.Finalizers, splitFinalizer)
			return ctrl.Result{}, r.Update(ctx, split)
		}
		split.Status.Phase = "Disabled"
		split.Status.AdmissionMode = api.KafkaAdmissionNormal
		setReadyCondition(&split.Status.Conditions, metav1.ConditionFalse, "Disabled", "Kafka split is disabled", split.Generation)
		return r.updateSplitStatus(ctx, split, before, false)
	}
	active := activeSplit(split)
	if split.Status.Phase == "CleaningApplication" {
		return r.finishDeactivation(ctx, split, before, active, deleting)
	}
	if split.Status.Phase == "RestoringApplication" {
		return r.reconcileRestoringApplication(ctx, split, before, active)
	}
	routes, err := r.closeRoutes(ctx, active)
	if err != nil {
		return ctrl.Result{}, err
	}
	prepared := preparedFromStatus(active)
	if split.Status.SplitterName != "" && split.Status.Phase != "StoppingSplitter" {
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
			ctx, split, split.Status.SplitterName, generation, splitterReplicas(active.Spec.Splitter),
		)
		if err != nil {
			return ctrl.Result{}, err
		}
		if !acknowledged {
			split.Status.Phase = "Pausing"
			setReadyCondition(&split.Status.Conditions, metav1.ConditionFalse, "Pausing", "waiting for splitters to pause between transactions", split.Generation)
			return r.updateSplitStatus(ctx, split, before, true)
		}
	}
	if routes > 0 {
		split.Status.Phase = "ClosingRoutes"
		setReadyCondition(&split.Status.Conditions, metav1.ConditionFalse, "ClosingRoutes", fmt.Sprintf("waiting for %d Kafka routes to drain", routes), split.Generation)
		return r.updateSplitStatus(ctx, split, before, true)
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
		split.Status.Phase = "DrainingApplication"
		setReadyCondition(
			&split.Status.Conditions, metav1.ConditionFalse, "DrainingApplication",
			fmt.Sprintf("waiting for application to consume %d shadow records", remaining), split.Generation,
		)
		return r.updateSplitStatus(ctx, split, before, true)
	}
	if split.Status.AdmissionMode != api.KafkaAdmissionBlocked {
		split.Status.AdmissionMode = api.KafkaAdmissionBlocked
		split.Status.Phase = "QuiescingApplication"
		setReadyCondition(&split.Status.Conditions, metav1.ConditionFalse, "QuiescingApplication", "blocking replacements before source handback", split.Generation)
		return r.updateSplitStatus(ctx, split, before, true)
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
		split.Status.Phase = "StoppingSplitter"
		setReadyCondition(
			&split.Status.Conditions,
			metav1.ConditionFalse,
			"StoppingSplitter",
			"waiting for splitters to leave the original group",
			split.Generation,
		)
		return r.updateSplitStatus(ctx, split, before, true)
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
		split.Status.Phase = "RestoringApplication"
		setReadyCondition(&split.Status.Conditions, metav1.ConditionFalse, "RestoringApplication", "normally configured Pods may resume", split.Generation)
		return r.updateSplitStatus(ctx, split, before, true)
	}
	split.Status.Phase = "CleaningApplication"
	setReadyCondition(&split.Status.Conditions, metav1.ConditionFalse, "CleaningApplication", "deleting managed application shadows", split.Generation)
	return r.updateSplitStatus(ctx, split, before, true)
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
		setReadyCondition(
			&split.Status.Conditions,
			metav1.ConditionFalse,
			"RestoringApplication",
			message,
			split.Generation,
		)
		return r.updateSplitStatus(ctx, split, before, true)
	}
	split.Status.Phase = "CleaningApplication"
	setReadyCondition(&split.Status.Conditions, metav1.ConditionFalse, "CleaningApplication", "deleting managed application shadows", split.Generation)
	return r.updateSplitStatus(ctx, split, before, true)
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
		setReadyCondition(&split.Status.Conditions, metav1.ConditionFalse, "CleanupPending", err.Error(), split.Generation)
		return r.updateSplitStatus(ctx, split, before, true)
	}
	if err := r.releaseOwnership(ctx, active); err != nil {
		return ctrl.Result{}, err
	}
	clearActiveStatus(&split.Status)
	if deleting {
		split.Finalizers = removeString(split.Finalizers, splitFinalizer)
		return ctrl.Result{}, r.Update(ctx, split)
	}
	if split.Spec.DesiredState == api.DesiredStateEnabled {
		split.Status.Phase = "Preparing"
		setReadyCondition(&split.Status.Conditions, metav1.ConditionFalse, "SpecChanged", "previous active snapshot is disabled; preparing the new generation", split.Generation)
		return r.updateSplitStatus(ctx, split, before, true)
	}
	split.Status.Phase = "Disabled"
	setReadyCondition(&split.Status.Conditions, metav1.ConditionFalse, "Disabled", "Kafka split is disabled", split.Generation)
	return r.updateSplitStatus(ctx, split, before, false)
}

func (r *SplitReconciler) closeRoutes(ctx context.Context, split *api.KafkaSplit) (int, error) {
	routes := new(api.KafkaRouteList)
	if err := r.List(ctx, routes, client.InNamespace(split.Namespace)); err != nil {
		return 0, err
	}
	remaining := 0
	for i := range routes.Items {
		route := &routes.Items[i]
		if route.Spec.SplitRef.Name != split.Name || route.Status.Phase == "Closed" {
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
	status.ActiveGeneration = 0
	status.ActiveSpec = nil
	status.AdmissionMode = api.KafkaAdmissionNormal
	status.ApplicationEnv = nil
	status.ApplicationTopics = nil
	status.ApplicationGroup = ""
	status.TransactionalID = ""
	status.SplitterName = ""
	status.Workloads = nil
	status.SourceTopics = nil
	status.Resources = nil
	status.RouteGeneration = 0
	status.Members = nil
	status.ApplicationLag = nil
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
	if split.Status.Phase != "Degraded" {
		split.Status.Phase = "Pending"
	}
	setReadyCondition(&split.Status.Conditions, metav1.ConditionFalse, reason, message, split.Generation)
	return r.updateSplitStatus(ctx, split, before, true)
}
