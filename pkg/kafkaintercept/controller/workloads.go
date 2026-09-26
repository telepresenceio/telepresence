package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"

	argorollouts "github.com/datawire/argo-rollouts-go-client/pkg/apis/rollouts/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apiMeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	api "github.com/telepresenceio/telepresence/v2/pkg/kafkaintercept/api/v1alpha1"
	"github.com/telepresenceio/telepresence/v2/pkg/kafkaintercept/runtimeconfig"
)

func (r *SplitReconciler) resolveWorkloads(ctx context.Context, split *api.KafkaSplit) ([]api.WorkloadReference, error) {
	selector, err := metav1.LabelSelectorAsSelector(&split.Spec.WorkloadSelector)
	if err != nil {
		return nil, err
	}
	options := []client.ListOption{client.InNamespace(split.Namespace), client.MatchingLabelsSelector{Selector: selector}}
	var refs []api.WorkloadReference

	deployments := new(appsv1.DeploymentList)
	if err := r.Workloads.List(ctx, deployments, options...); err != nil {
		return nil, fmt.Errorf("list Deployments: %w", err)
	}
	for i := range deployments.Items {
		refs = append(refs, workloadReference("apps/v1", "Deployment", &deployments.Items[i]))
	}

	statefulSets := new(appsv1.StatefulSetList)
	if err := r.Workloads.List(ctx, statefulSets, options...); err != nil {
		return nil, fmt.Errorf("list StatefulSets: %w", err)
	}
	for i := range statefulSets.Items {
		refs = append(refs, workloadReference("apps/v1", "StatefulSet", &statefulSets.Items[i]))
	}

	replicaSets := new(appsv1.ReplicaSetList)
	if err := r.Workloads.List(ctx, replicaSets, options...); err != nil {
		return nil, fmt.Errorf("list ReplicaSets: %w", err)
	}
	for i := range replicaSets.Items {
		if metav1.GetControllerOf(&replicaSets.Items[i]) == nil {
			refs = append(refs, workloadReference("apps/v1", "ReplicaSet", &replicaSets.Items[i]))
		}
	}

	rollouts := new(argorollouts.RolloutList)
	if err := r.Workloads.List(ctx, rollouts, options...); err != nil && !apiMeta.IsNoMatchError(err) && !apierrors.IsNotFound(err) {
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

func (r *SplitReconciler) validateWorkloadEnvironment(
	ctx context.Context,
	split *api.KafkaSplit,
	workloads []api.WorkloadReference,
) (int32, error) {
	var desired int32
	for _, workload := range workloads {
		template, _, replicas, err := r.workloadTemplate(ctx, split.Namespace, workload)
		if err != nil {
			return 0, err
		}
		desired += replicas
		container := findContainer(template.Spec.Containers, split.Spec.Container)
		if container == nil {
			return 0, fmt.Errorf("%s %s has no container %s", workload.Kind, workload.Name, split.Spec.Container)
		}
		if err := validateContainerBindings(container, split); err != nil {
			return 0, fmt.Errorf("%s %s: %w", workload.Kind, workload.Name, err)
		}
	}
	return desired, r.validateSplitComposition(ctx, split, workloads)
}

func (r *SplitReconciler) workloadTemplate(
	ctx context.Context,
	namespace string,
	ref api.WorkloadReference,
) (*corev1.PodTemplateSpec, *metav1.LabelSelector, int32, error) {
	key := client.ObjectKey{Namespace: namespace, Name: ref.Name}
	switch ref.Kind {
	case "Deployment":
		object := new(appsv1.Deployment)
		if err := r.Workloads.Get(ctx, key, object); err != nil {
			return nil, nil, 0, err
		}
		return &object.Spec.Template, object.Spec.Selector, replicas(object.Spec.Replicas), nil
	case "StatefulSet":
		object := new(appsv1.StatefulSet)
		if err := r.Workloads.Get(ctx, key, object); err != nil {
			return nil, nil, 0, err
		}
		return &object.Spec.Template, object.Spec.Selector, replicas(object.Spec.Replicas), nil
	case "ReplicaSet":
		object := new(appsv1.ReplicaSet)
		if err := r.Workloads.Get(ctx, key, object); err != nil {
			return nil, nil, 0, err
		}
		return &object.Spec.Template, object.Spec.Selector, replicas(object.Spec.Replicas), nil
	case "Rollout":
		object := new(argorollouts.Rollout)
		if err := r.Workloads.Get(ctx, key, object); err != nil {
			return nil, nil, 0, err
		}
		return &object.Spec.Template, object.Spec.Selector, replicas(object.Spec.Replicas), nil
	default:
		return nil, nil, 0, fmt.Errorf("unsupported workload kind %s", ref.Kind)
	}
}

func replicas(value *int32) int32 {
	if value == nil {
		return 1
	}
	return *value
}

func validateContainerBindings(container *corev1.Container, split *api.KafkaSplit) error {
	bindings := make(map[string]string)
	application := split.Spec.Application
	if application.TopicEnv != "" {
		bindings[application.TopicEnv] = strings.Join(split.Spec.Source.Topics, application.TopicSeparator)
	} else {
		for _, binding := range application.TopicBindings {
			bindings[binding.Env] = binding.Source
		}
	}
	bindings[application.GroupEnv] = split.Spec.Source.Group
	for name, expected := range bindings {
		env, ok := explicitEnv(container.Env, name)
		if !ok || env.ValueFrom != nil {
			return fmt.Errorf("environment variable %s must be an explicit literal", name)
		}
		if env.Value != expected {
			return fmt.Errorf("environment variable %s is %q, expected %q", name, env.Value, expected)
		}
	}
	for _, name := range []string{application.IsolationLevelEnv, application.TransactionalIDEnv} {
		if name == "" {
			continue
		}
		env, ok := explicitEnv(container.Env, name)
		if !ok || env.ValueFrom != nil {
			return fmt.Errorf("environment variable %s must be an explicit literal", name)
		}
	}
	return nil
}

func explicitEnv(envs []corev1.EnvVar, name string) (corev1.EnvVar, bool) {
	for _, env := range envs {
		if env.Name == name {
			return env, true
		}
	}
	return corev1.EnvVar{}, false
}

func (r *SplitReconciler) validateSplitComposition(
	ctx context.Context,
	split *api.KafkaSplit,
	workloads []api.WorkloadReference,
) error {
	others := new(api.KafkaSplitList)
	if err := r.List(ctx, others, client.InNamespace(split.Namespace)); err != nil {
		return err
	}
	ours := bindingNames(split)
	for i := range others.Items {
		other := &others.Items[i]
		if other.Name == split.Name || other.Spec.DesiredState != api.DesiredStateEnabled || other.Spec.Container != split.Spec.Container {
			continue
		}
		otherWorkloads := other.Status.Workloads
		if len(otherWorkloads) == 0 {
			resolved, err := r.resolveWorkloads(ctx, other)
			if err != nil {
				continue
			}
			otherWorkloads = resolved
		}
		if !workloadsOverlap(workloads, otherWorkloads) {
			continue
		}
		for name := range bindingNames(other) {
			if _, ok := ours[name]; ok {
				ours := split.CreationTimestamp.Time
				theirs := other.CreationTimestamp.Time
				if theirs.Before(ours) || (theirs.Equal(ours) && other.Name < split.Name) {
					return fmt.Errorf("KafkaSplit %s also overrides %s in the selected container", other.Name, name)
				}
			}
		}
	}
	return nil
}

func bindingNames(split *api.KafkaSplit) map[string]struct{} {
	result := map[string]struct{}{
		split.Spec.Application.GroupEnv:          {},
		split.Spec.Application.IsolationLevelEnv: {},
	}
	if name := split.Spec.Application.TopicEnv; name != "" {
		result[name] = struct{}{}
	}
	for _, binding := range split.Spec.Application.TopicBindings {
		result[binding.Env] = struct{}{}
	}
	if name := split.Spec.Application.TransactionalIDEnv; name != "" {
		result[name] = struct{}{}
	}
	for name := range split.Spec.Application.ShadowCredentials {
		result[name] = struct{}{}
	}
	return result
}

func workloadsOverlap(a, b []api.WorkloadReference) bool {
	for _, left := range a {
		for _, right := range b {
			if left.UID == right.UID {
				return true
			}
		}
	}
	return false
}

func (r *SplitReconciler) replacePods(
	ctx context.Context,
	split *api.KafkaSplit,
	desired int32,
	generation int64,
) (bool, string, error) {
	resolver := &replicaSetResolver{reader: r.Workloads}
	seen := make(map[types.UID]struct{})
	ready := int32(0)
	stale := 0
	for _, workload := range split.Status.Workloads {
		_, selector, _, err := r.workloadTemplate(ctx, split.Namespace, workload)
		if err != nil {
			return false, "", err
		}
		labelSelector, err := metav1.LabelSelectorAsSelector(selector)
		if err != nil {
			return false, "", err
		}
		pods := new(corev1.PodList)
		if err := r.Workloads.List(
			ctx, pods, client.InNamespace(split.Namespace), client.MatchingLabelsSelector{Selector: labelSelector},
		); err != nil {
			return false, "", err
		}
		for i := range pods.Items {
			pod := &pods.Items[i]
			if _, ok := seen[pod.UID]; ok {
				continue
			}
			matches, err := podMatchesSnapshot(ctx, resolver, pod, split.Status.Workloads)
			if err != nil {
				return false, "", err
			}
			if !matches {
				continue
			}
			seen[pod.UID] = struct{}{}
			if !podHasGeneration(pod, split.Name, generation) {
				stale++
				if pod.DeletionTimestamp == nil {
					eviction := &policyv1.Eviction{ObjectMeta: metav1.ObjectMeta{Name: pod.Name, Namespace: pod.Namespace}}
					if err := r.SubResource("eviction").Create(ctx, pod, eviction); err != nil && !apierrors.IsNotFound(err) {
						return false, fmt.Sprintf("eviction of Pod %s is blocked: %v", pod.Name, err), nil
					}
				}
				continue
			}
			if podReady(pod) {
				ready++
			}
		}
	}
	if stale > 0 {
		return false, fmt.Sprintf("waiting for %d Pods to be replaced", stale), nil
	}
	if ready < desired {
		return false, fmt.Sprintf("waiting for redirected Pods (%d/%d ready)", ready, desired), nil
	}
	return true, "", nil
}

func podHasGeneration(pod *corev1.Pod, split string, generation int64) bool {
	encoded := pod.Annotations[runtimeconfig.ActiveAnnotation]
	if encoded == "" {
		return generation == 0
	}
	values := make(map[string]string)
	if json.Unmarshal([]byte(encoded), &values) != nil {
		return false
	}
	value, ok := values[split]
	if generation == 0 {
		return !ok
	}
	return ok && value == fmt.Sprint(generation)
}

func podReady(pod *corev1.Pod) bool {
	if pod.DeletionTimestamp != nil {
		return false
	}
	return slices.ContainsFunc(pod.Status.Conditions, func(condition corev1.PodCondition) bool {
		return condition.Type == corev1.PodReady && condition.Status == corev1.ConditionTrue
	})
}
