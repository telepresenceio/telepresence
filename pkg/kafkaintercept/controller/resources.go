package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/telepresenceio/telepresence/v2/pkg/kafkaintercept"
	api "github.com/telepresenceio/telepresence/v2/pkg/kafkaintercept/api/v1alpha1"
	"github.com/telepresenceio/telepresence/v2/pkg/kafkaintercept/broker"
	"github.com/telepresenceio/telepresence/v2/pkg/kafkaintercept/runtimeconfig"
)

func (r *SplitReconciler) acquireOwnership(ctx context.Context, split *api.KafkaSplit) error {
	key := client.ObjectKey{Namespace: r.providerNamespace(), Name: broker.OwnershipLeaseName(split)}
	holder := string(split.UID)
	now := metav1.NewMicroTime(time.Now())
	duration := int32(60)
	lease := new(coordinationv1.Lease)
	if err := r.Get(ctx, key, lease); apierrors.IsNotFound(err) {
		return r.Create(ctx, &coordinationv1.Lease{
			ObjectMeta: metav1.ObjectMeta{Namespace: key.Namespace, Name: key.Name},
			Spec: coordinationv1.LeaseSpec{
				HolderIdentity: &holder, LeaseDurationSeconds: &duration, RenewTime: &now,
			},
		})
	} else if err != nil {
		return err
	}
	if lease.Spec.HolderIdentity == nil || *lease.Spec.HolderIdentity != holder {
		return fmt.Errorf("kafka consumer group %s is owned by another KafkaSplit", split.Spec.Source.Group)
	}
	lease.Spec.RenewTime = &now
	lease.Spec.LeaseDurationSeconds = &duration
	return r.Update(ctx, lease)
}

func (r *SplitReconciler) releaseOwnership(ctx context.Context, split *api.KafkaSplit) error {
	key := client.ObjectKey{Namespace: r.providerNamespace(), Name: broker.OwnershipLeaseName(split)}
	lease := new(coordinationv1.Lease)
	if err := r.Get(ctx, key, lease); apierrors.IsNotFound(err) {
		return nil
	} else if err != nil {
		return err
	}
	if lease.Spec.HolderIdentity != nil && *lease.Spec.HolderIdentity != string(split.UID) {
		return nil
	}
	return client.IgnoreNotFound(r.Delete(ctx, lease))
}

func activeSpec(split *api.KafkaSplit) *api.KafkaActiveSpec {
	spec := split.Spec.DeepCopy()
	return &api.KafkaActiveSpec{
		Container: spec.Container, Connection: spec.Connection, Source: spec.Source,
		Application: spec.Application, Splitter: spec.Splitter, Shadows: spec.Shadows,
	}
}

func activeSplit(split *api.KafkaSplit) *api.KafkaSplit {
	if split.Status.ActiveSpec == nil {
		return split
	}
	clone := split.DeepCopy()
	clone.Spec.Container = clone.Status.ActiveSpec.Container
	clone.Spec.Connection = clone.Status.ActiveSpec.Connection
	clone.Spec.Source = clone.Status.ActiveSpec.Source
	clone.Spec.Application = clone.Status.ActiveSpec.Application
	clone.Spec.Splitter = clone.Status.ActiveSpec.Splitter
	clone.Spec.Shadows = clone.Status.ActiveSpec.Shadows
	return clone
}

func applicationEnvironment(
	source api.KafkaSourceSpec,
	application api.KafkaApplicationSpec,
	topics map[string]string,
	group string,
) map[string]string {
	env := make(map[string]string)
	if application.TopicEnv != "" {
		values := make([]string, 0, len(source.Topics))
		for _, topic := range source.Topics {
			values = append(values, topics[topic])
		}
		env[application.TopicEnv] = strings.Join(values, application.TopicSeparator)
	} else {
		for _, binding := range application.TopicBindings {
			env[binding.Env] = topics[binding.Source]
		}
	}
	env[application.GroupEnv] = group
	env[application.IsolationLevelEnv] = "read_committed"
	return env
}

func (r *SplitReconciler) desiredRoutingTable(
	ctx context.Context,
	split *api.KafkaSplit,
	paused bool,
) (kafkaintercept.RoutingTable, error) {
	routes := new(api.KafkaRouteList)
	if err := r.List(
		ctx, routes, client.InNamespace(split.Namespace), client.MatchingFields{routeSplitRefIndexField: split.Name},
	); err != nil {
		return kafkaintercept.RoutingTable{}, err
	}
	sort.Slice(routes.Items, func(i, j int) bool { return routes.Items[i].Name < routes.Items[j].Name })
	table := kafkaintercept.RoutingTable{Paused: paused}
	for i := range routes.Items {
		route := &routes.Items[i]
		if route.Spec.DesiredState != api.RouteStateActive ||
			!route.Spec.ExpiresAt.After(time.Now()) || len(route.Status.Topics) == 0 ||
			(route.Status.Phase != api.RoutePhaseStaged && route.Status.Phase != api.RoutePhaseReady) {
			continue
		}
		table.Routes = append(table.Routes, kafkaintercept.Route{
			ID: route.Name, Predicate: predicateFromAPI(route.Spec.Predicate), Topics: maps.Clone(route.Status.Topics),
		})
	}
	return table, nil
}

func predicateFromAPI(value api.KafkaRoutePredicate) kafkaintercept.Predicate {
	result := kafkaintercept.Predicate{
		Headers: make(map[string][]byte, len(value.Headers)), Key: slices.Clone(value.Key), KeyPrefix: slices.Clone(value.KeyPrefix),
	}
	for _, header := range value.Headers {
		result.Headers[header.Name] = slices.Clone(header.Value)
	}
	return result
}

func (r *SplitReconciler) ensureProviderResources(
	ctx context.Context,
	split *api.KafkaSplit,
	prepared broker.Prepared,
	table kafkaintercept.RoutingTable,
) (uint64, error) {
	name := prepared.Names.KubernetesName
	key := client.ObjectKey{Namespace: r.providerNamespace(), Name: name}
	configMap := new(corev1.ConfigMap)
	err := r.Get(ctx, key, configMap)
	if err != nil && !apierrors.IsNotFound(err) {
		return 0, err
	}
	if apierrors.IsNotFound(err) {
		table.Generation = 1
		configMap = &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{
			Namespace: key.Namespace, Name: key.Name,
			Labels: map[string]string{
				runtimeconfig.SplitLabel:          name,
				runtimeconfig.SplitNamespaceLabel: split.Namespace,
				runtimeconfig.SplitNameLabel:      split.Name,
			},
		}}
	} else {
		current, parseErr := runtimeconfig.RoutingFromConfigMap(configMap)
		if parseErr != nil {
			return 0, parseErr
		}
		table.Generation = current.Generation
		current.Generation = 0
		comparison := table
		comparison.Generation = 0
		if !reflect.DeepEqual(current, comparison) {
			table.Generation++
		}
	}
	configBytes := []byte(configMap.Data[runtimeconfig.ConfigDataKey])
	if configMap.ResourceVersion == "" {
		runtime := runtimeconfig.Config{
			Namespace: split.Namespace, Connection: split.Spec.Connection, Group: split.Spec.Source.Group,
			InstanceIDPrefix: prepared.Names.SplitterInstance, TransactionalIDPrefix: prepared.Names.SplitterTransactional,
			AppTopics: maps.Clone(prepared.ApplicationTopics), OffsetReset: split.Spec.Source.OffsetReset,
			BatchSize: int(split.Spec.Splitter.EffectiveBatchSize()), Routes: table,
			Control: &runtimeconfig.Control{
				Namespace: key.Namespace, ConfigMap: key.Name, MemberLeasePrefix: name + "-", Split: name,
				SplitName: split.Name,
			},
		}
		configBytes, err = json.Marshal(runtime)
		if err != nil {
			return 0, err
		}
	}
	routingBytes, err := json.Marshal(table)
	if err != nil {
		return 0, err
	}
	data := map[string]string{
		runtimeconfig.ConfigDataKey: string(configBytes), runtimeconfig.RoutingDataKey: string(routingBytes),
	}
	if configMap.ResourceVersion == "" {
		configMap.Data = data
		if err := r.Create(ctx, configMap); err != nil {
			return 0, err
		}
	} else if !reflect.DeepEqual(configMap.Data, data) {
		configMap.Data = data
		if err := r.Update(ctx, configMap); err != nil {
			return 0, err
		}
	}
	if err := r.ensureSplitterService(ctx, split, key); err != nil {
		return 0, err
	}
	if err := r.ensureSplitterStatefulSet(ctx, split, key, configBytes); err != nil {
		return 0, err
	}
	return table.Generation, nil
}

func (r *SplitReconciler) ensureSplitterService(ctx context.Context, split *api.KafkaSplit, key client.ObjectKey) error {
	selector := map[string]string{
		"app.kubernetes.io/name": runtimeconfig.SplitterName, runtimeconfig.SplitLabel: key.Name,
	}
	labels := maps.Clone(selector)
	labels[runtimeconfig.SplitNamespaceLabel] = split.Namespace
	labels[runtimeconfig.SplitNameLabel] = split.Name
	expected := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Namespace: key.Namespace, Name: key.Name, Labels: labels},
		Spec: corev1.ServiceSpec{
			ClusterIP: corev1.ClusterIPNone,
			Selector:  selector,
		},
	}
	current := new(corev1.Service)
	if err := r.Get(ctx, key, current); apierrors.IsNotFound(err) {
		return r.Create(ctx, expected)
	} else if err != nil {
		return err
	}
	before := current.DeepCopy()
	current.Labels = expected.Labels
	current.Spec.Selector = expected.Spec.Selector
	if reflect.DeepEqual(before, current) {
		return nil
	}
	return r.Update(ctx, current)
}

func (r *SplitReconciler) ensureSplitterStatefulSet(
	ctx context.Context,
	split *api.KafkaSplit,
	key client.ObjectKey,
	config []byte,
) error {
	image := r.ProviderImage
	if image == "" {
		image = "telepresence-kafka:latest"
	}
	serviceAccount := r.ServiceAccount
	if serviceAccount == "" {
		serviceAccount = runtimeconfig.ProviderName
	}
	selector := map[string]string{
		"app.kubernetes.io/name": runtimeconfig.SplitterName, runtimeconfig.SplitLabel: key.Name,
	}
	labels := maps.Clone(selector)
	labels[runtimeconfig.SplitNamespaceLabel] = split.Namespace
	labels[runtimeconfig.SplitNameLabel] = split.Name
	replicas := split.Spec.Splitter.EffectiveReplicas()
	checksum := sha256.Sum256(config)
	expected := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Namespace: key.Namespace, Name: key.Name, Labels: labels},
		Spec: appsv1.StatefulSetSpec{
			ServiceName: key.Name, Replicas: &replicas,
			Selector: &metav1.LabelSelector{MatchLabels: maps.Clone(selector)},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: maps.Clone(selector), Annotations: map[string]string{runtimeconfig.ConfigAnnotation: hex.EncodeToString(checksum[:])},
				},
				Spec: corev1.PodSpec{
					ServiceAccountName: serviceAccount,
					SecurityContext:    &corev1.PodSecurityContext{RunAsNonRoot: ptr.To(true)},
					Containers: []corev1.Container{{
						Name: "splitter", Image: image, Args: []string{"splitter"},
						Env: []corev1.EnvVar{{
							Name: "POD_NAME", ValueFrom: &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.name"}},
						}},
						SecurityContext: &corev1.SecurityContext{
							AllowPrivilegeEscalation: ptr.To(false), ReadOnlyRootFilesystem: ptr.To(true),
							Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
						},
						VolumeMounts: []corev1.VolumeMount{{Name: "config", MountPath: "/var/run/telepresence-kafka", ReadOnly: true}},
					}},
					Volumes: []corev1.Volume{{
						Name: "config", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{
							LocalObjectReference: corev1.LocalObjectReference{Name: key.Name},
							Items:                []corev1.KeyToPath{{Key: runtimeconfig.ConfigDataKey, Path: "config.json"}},
						}},
					}},
				},
			},
		},
	}
	current := new(appsv1.StatefulSet)
	if err := r.Get(ctx, key, current); apierrors.IsNotFound(err) {
		return r.Create(ctx, expected)
	} else if err != nil {
		return err
	}
	before := current.DeepCopy()
	current.Spec.Replicas = expected.Spec.Replicas
	current.Spec.Template = expected.Spec.Template
	if reflect.DeepEqual(before.Spec, current.Spec) {
		return nil
	}
	return r.Update(ctx, current)
}

func (r *SplitReconciler) membersAcknowledged(
	ctx context.Context,
	split *api.KafkaSplit,
	name string,
	generation uint64,
	replicas int32,
) (bool, error) {
	members, err := splitterMemberStatus(ctx, r.Client, r.providerNamespace(), name, replicas)
	if err != nil {
		return false, err
	}
	split.Status.Members = members
	return membersReady(members, replicas, generation), nil
}

// memberStaleAfter is how old a member Lease's RenewTime can be before it is
// reported unhealthy. Must agree with heartbeatInterval and
// memberLeaseDurationSeconds in cmd/telepresence-kafka/control.go.
const memberStaleAfter = 45 * time.Second

// splitterMemberStatus lists the member Leases for a splitter, deleting the
// ones left behind by ordinals a replica reduction retired.
func splitterMemberStatus(
	ctx context.Context,
	c client.Client,
	namespace string,
	name string,
	replicas int32,
) ([]api.KafkaSplitterMemberStatus, error) {
	leases := new(coordinationv1.LeaseList)
	if err := c.List(
		ctx, leases, client.InNamespace(namespace), client.MatchingLabels{runtimeconfig.SplitLabel: name},
	); err != nil {
		return nil, err
	}
	prefix := name + "-"
	now := time.Now()
	members := make([]api.KafkaSplitterMemberStatus, 0, len(leases.Items))
	for i := range leases.Items {
		lease := &leases.Items[i]
		ordinal, ok := strings.CutPrefix(lease.Name, prefix)
		if !ok {
			continue
		}
		if n, err := strconv.Atoi(ordinal); err != nil || n >= int(replicas) {
			if err == nil {
				if delErr := client.IgnoreNotFound(c.Delete(ctx, lease)); delErr != nil {
					return nil, delErr
				}
			}
			continue
		}
		ack, _ := strconv.ParseUint(lease.Annotations[runtimeconfig.GenerationAnnotation], 10, 64)
		healthy, _ := strconv.ParseBool(lease.Annotations[runtimeconfig.HealthyAnnotation])
		lastSeen := metav1.Time{}
		if lease.Spec.RenewTime == nil || now.Sub(lease.Spec.RenewTime.Time) > memberStaleAfter {
			healthy = false
		} else {
			lastSeen = metav1.NewTime(lease.Spec.RenewTime.Time)
		}
		members = append(members, api.KafkaSplitterMemberStatus{
			Name: lease.Name, Generation: int64(ack), Healthy: healthy,
			LastSeen: lastSeen,
		})
	}
	sort.Slice(members, func(i, j int) bool { return members[i].Name < members[j].Name })
	return members, nil
}

func (r *SplitReconciler) deleteProviderResources(ctx context.Context, split *api.KafkaSplit) (bool, error) {
	if split.Status.SplitterName == "" {
		return true, nil
	}
	key := client.ObjectKey{Namespace: r.providerNamespace(), Name: split.Status.SplitterName}
	statefulSet := new(appsv1.StatefulSet)
	if err := r.Get(ctx, key, statefulSet); err == nil {
		if statefulSet.DeletionTimestamp == nil {
			if err := r.Delete(ctx, statefulSet); err != nil {
				return false, err
			}
		}
		return false, nil
	} else if !apierrors.IsNotFound(err) {
		return false, err
	}
	service := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Namespace: key.Namespace, Name: key.Name}}
	if err := r.Delete(ctx, service); err != nil && !apierrors.IsNotFound(err) {
		return false, err
	}
	configMap := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: key.Namespace, Name: key.Name}}
	if err := r.Delete(ctx, configMap); err != nil && !apierrors.IsNotFound(err) {
		return false, err
	}
	leases := new(coordinationv1.LeaseList)
	if err := r.List(ctx, leases, client.InNamespace(key.Namespace), client.MatchingLabels{runtimeconfig.SplitLabel: key.Name}); err != nil {
		return false, err
	}
	for i := range leases.Items {
		if err := r.Delete(ctx, &leases.Items[i]); err != nil && !apierrors.IsNotFound(err) {
			return false, err
		}
	}
	return true, nil
}
