package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"sort"
	"strconv"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	api "github.com/telepresenceio/telepresence/v2/pkg/kafkaintercept/api/v1alpha1"
	"github.com/telepresenceio/telepresence/v2/pkg/kafkaintercept/runtimeconfig"
)

// podUIDEnvName names the environment variable that carries a Pod's UID.
const podUIDEnvName = "TELEPRESENCE_KAFKA_POD_UID"

// RegisterWebhooks installs validation handlers on server. reader lists
// KafkaSplits; workloads resolves a Pod's ReplicaSet owner and is backed by
// NamespaceInformers.
func RegisterWebhooks(
	server interface{ Register(string, http.Handler) },
	reader client.Reader,
	workloads client.Reader,
	providerNamespace string,
) {
	server.Register("/split", &admission.Webhook{Handler: splitValidator{}})
	server.Register("/route", &admission.Webhook{Handler: routeValidator{}})
	server.Register("/pod", &admission.Webhook{Handler: podMutator{reader: reader, workloads: workloads, providerNamespace: providerNamespace}})
}

type splitValidator struct{}

func (splitValidator) Handle(_ context.Context, request admission.Request) admission.Response {
	split := new(api.KafkaSplit)
	if err := json.Unmarshal(request.Object.Raw, split); err != nil {
		return admission.Errored(http.StatusBadRequest, err)
	}
	if err := split.Validate(); err != nil {
		return admission.Denied(err.Error())
	}
	return admission.Allowed("KafkaSplit is valid")
}

type routeValidator struct{}

func (routeValidator) Handle(_ context.Context, request admission.Request) admission.Response {
	route := new(api.KafkaRoute)
	if err := json.Unmarshal(request.Object.Raw, route); err != nil {
		return admission.Errored(http.StatusBadRequest, err)
	}
	if err := route.Validate(); err != nil {
		return admission.Denied(err.Error())
	}
	if request.UserInfo.Username == "" {
		return admission.Denied("KafkaRoute creator identity is missing")
	}
	return admission.Allowed(fmt.Sprintf("KafkaRoute accepted for %s", request.UserInfo.Username))
}

type podMutator struct {
	reader            client.Reader
	workloads         client.Reader
	providerNamespace string
}

func (m podMutator) Handle(ctx context.Context, request admission.Request) admission.Response {
	if request.Namespace == m.providerNamespace {
		return admission.Allowed("Kafka provider namespace is excluded")
	}
	pod := new(corev1.Pod)
	if err := json.Unmarshal(request.Object.Raw, pod); err != nil {
		return admission.Errored(http.StatusBadRequest, err)
	}
	originalNamespace := pod.Namespace
	if originalNamespace == "" {
		pod.Namespace = request.Namespace
	}

	splits := new(api.KafkaSplitList)
	if err := m.reader.List(ctx, splits, client.InNamespace(request.Namespace)); err != nil {
		return admission.Errored(http.StatusInternalServerError, fmt.Errorf("list KafkaSplits: %w", err))
	}
	sort.Slice(splits.Items, func(i, j int) bool { return splits.Items[i].Name < splits.Items[j].Name })

	original := pod.DeepCopy()
	generations := make(map[string]string)
	envOwners := make(map[string]string)
	resolver := &replicaSetResolver{reader: m.workloads}
	for i := range splits.Items {
		split := &splits.Items[i]
		if split.Status.ActiveGeneration == 0 || split.Status.AdmissionMode == api.KafkaAdmissionNormal {
			continue
		}
		matches, err := podMatchesSnapshot(ctx, resolver, pod, split.Status.Workloads)
		if err != nil {
			return admission.Errored(http.StatusInternalServerError, err)
		}
		if !matches {
			continue
		}
		containerName := split.Spec.Container
		application := split.Spec.Application
		if split.Status.ActiveSpec != nil {
			containerName = split.Status.ActiveSpec.Container
			application = split.Status.ActiveSpec.Application
		}
		container := findContainer(pod.Spec.Containers, containerName)
		if container == nil {
			return admission.Denied(fmt.Sprintf("KafkaSplit %s selected Pod without container %s", split.Name, containerName))
		}
		for name, value := range split.Status.ApplicationEnv {
			if response := claimEnv(envOwners, split.Name, name); response != nil {
				return *response
			}
			setEnv(container, corev1.EnvVar{Name: name, Value: value})
		}
		for name, source := range application.ShadowCredentials {
			if response := claimEnv(envOwners, split.Name, name); response != nil {
				return *response
			}
			setEnv(container, valueSourceEnvVar(name, source))
		}
		if split.Status.TransactionalID != "" && application.TransactionalIDEnv != "" {
			name := application.TransactionalIDEnv
			if response := claimEnv(envOwners, split.Name, name); response != nil {
				return *response
			}
			setEnv(container, podUIDEnvVar())
			setLiteralEnvLast(container, name, split.Status.TransactionalID+".$("+podUIDEnvName+")")
		}
		generations[split.Name] = strconv.FormatInt(split.Status.ActiveGeneration, 10)
	}
	if len(generations) == 0 || reflect.DeepEqual(original, pod) {
		return admission.Allowed("Pod has no active Kafka split")
	}
	encoded, err := json.Marshal(generations)
	if err != nil {
		return admission.Errored(http.StatusInternalServerError, err)
	}
	if pod.Annotations == nil {
		pod.Annotations = make(map[string]string)
	}
	pod.Annotations[runtimeconfig.ActiveAnnotation] = string(encoded)
	if originalNamespace == "" {
		pod.Namespace = ""
	}
	mutated, err := json.Marshal(pod)
	if err != nil {
		return admission.Errored(http.StatusInternalServerError, err)
	}
	return admission.PatchResponseFromRaw(request.Object.Raw, mutated)
}

func claimEnv(owners map[string]string, split, name string) *admission.Response {
	if prior, ok := owners[name]; ok {
		response := admission.Denied(fmt.Sprintf("KafkaSplits %s and %s both override %s", prior, split, name))
		return &response
	}
	owners[name] = split
	return nil
}

func findContainer(containers []corev1.Container, name string) *corev1.Container {
	for i := range containers {
		if containers[i].Name == name {
			return &containers[i]
		}
	}
	return nil
}

// setEnv replaces the container's existing env var of the same name, or appends it.
func setEnv(container *corev1.Container, env corev1.EnvVar) {
	for i := range container.Env {
		if container.Env[i].Name == env.Name {
			container.Env[i] = env
			return
		}
	}
	container.Env = append(container.Env, env)
}

func setLiteralEnvLast(container *corev1.Container, name, value string) {
	for i := range container.Env {
		if container.Env[i].Name == name {
			container.Env = append(container.Env[:i], container.Env[i+1:]...)
			break
		}
	}
	container.Env = append(container.Env, corev1.EnvVar{Name: name, Value: value})
}

func valueSourceEnvVar(name string, source api.ValueSource) corev1.EnvVar {
	env := corev1.EnvVar{Name: name, Value: source.Value}
	if source.SecretKeyRef != nil {
		env.Value = ""
		env.ValueFrom = &corev1.EnvVarSource{SecretKeyRef: source.SecretKeyRef.DeepCopy()}
	}
	return env
}

func podUIDEnvVar() corev1.EnvVar {
	return corev1.EnvVar{
		Name: podUIDEnvName,
		ValueFrom: &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{
			APIVersion: "v1", FieldPath: "metadata.uid",
		}},
	}
}

// replicaSetResolver memoizes ReplicaSet-controller lookups by ReplicaSet name.
type replicaSetResolver struct {
	reader client.Reader
	cache  map[string]*metav1.OwnerReference
}

func (resolver *replicaSetResolver) parentOf(ctx context.Context, namespace, name string) (*metav1.OwnerReference, error) {
	if resolver.cache == nil {
		resolver.cache = make(map[string]*metav1.OwnerReference)
	}
	if parent, ok := resolver.cache[name]; ok {
		return parent, nil
	}
	rs := new(appsv1.ReplicaSet)
	if err := resolver.reader.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, rs); err != nil {
		return nil, fmt.Errorf("resolve ReplicaSet owner %s/%s: %w", namespace, name, err)
	}
	parent := metav1.GetControllerOf(rs)
	resolver.cache[name] = parent
	return parent, nil
}

func podMatchesSnapshot(
	ctx context.Context,
	resolver *replicaSetResolver,
	pod *corev1.Pod,
	workloads []api.WorkloadReference,
) (bool, error) {
	owner := metav1.GetControllerOf(pod)
	if owner == nil {
		return false, nil
	}
	for _, workload := range workloads {
		if workload.UID == owner.UID {
			return true, nil
		}
	}
	if owner.Kind != "ReplicaSet" {
		return false, nil
	}
	parent, err := resolver.parentOf(ctx, pod.Namespace, owner.Name)
	if err != nil {
		return false, err
	}
	if parent == nil {
		return false, nil
	}
	for _, workload := range workloads {
		if workload.UID == parent.UID {
			return true, nil
		}
	}
	return false, nil
}
