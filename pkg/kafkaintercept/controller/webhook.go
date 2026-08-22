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
)

// RegisterWebhooks installs validation handlers on server.
func RegisterWebhooks(server interface{ Register(string, http.Handler) }, reader client.Reader) {
	server.Register("/split", &admission.Webhook{Handler: splitValidator{}})
	server.Register("/route", &admission.Webhook{Handler: routeValidator{}})
	server.Register("/pod", &admission.Webhook{Handler: podMutator{reader: reader}})
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
	reader client.Reader
}

func (m podMutator) Handle(ctx context.Context, request admission.Request) admission.Response {
	pod := new(corev1.Pod)
	if err := json.Unmarshal(request.Object.Raw, pod); err != nil {
		return admission.Errored(http.StatusBadRequest, err)
	}
	if pod.Labels["app.kubernetes.io/name"] == "telepresence-kafka" {
		return admission.Allowed("Kafka provider Pod is excluded")
	}

	splits := new(api.KafkaSplitList)
	if err := m.reader.List(ctx, splits, client.InNamespace(request.Namespace)); err != nil {
		return admission.Errored(http.StatusInternalServerError, fmt.Errorf("list KafkaSplits: %w", err))
	}
	sort.Slice(splits.Items, func(i, j int) bool { return splits.Items[i].Name < splits.Items[j].Name })

	original := pod.DeepCopy()
	generations := make(map[string]string)
	envOwners := make(map[string]string)
	for i := range splits.Items {
		split := &splits.Items[i]
		if split.Status.ActiveGeneration == 0 || split.Status.AdmissionMode == api.KafkaAdmissionNormal {
			continue
		}
		matches, err := podMatchesSnapshot(ctx, m.reader, pod, split.Status.Workloads)
		if err != nil {
			return admission.Errored(http.StatusInternalServerError, err)
		}
		if !matches {
			continue
		}
		if split.Status.AdmissionMode == api.KafkaAdmissionBlocked {
			return admission.Denied(fmt.Sprintf("KafkaSplit %s is changing source ownership", split.Name))
		}
		container := findContainer(pod.Spec.Containers, split.Spec.Container)
		if container == nil {
			return admission.Denied(fmt.Sprintf("KafkaSplit %s selected Pod without container %s", split.Name, split.Spec.Container))
		}
		for name, value := range split.Status.ApplicationEnv {
			if prior, ok := envOwners[name]; ok {
				return admission.Denied(fmt.Sprintf("KafkaSplits %s and %s both override %s", prior, split.Name, name))
			}
			envOwners[name] = split.Name
			setLiteralEnv(container, name, value)
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
	pod.Annotations["kafka.telepresence.io/active-generations"] = string(encoded)
	mutated, err := json.Marshal(pod)
	if err != nil {
		return admission.Errored(http.StatusInternalServerError, err)
	}
	return admission.PatchResponseFromRaw(request.Object.Raw, mutated)
}

func findContainer(containers []corev1.Container, name string) *corev1.Container {
	for i := range containers {
		if containers[i].Name == name {
			return &containers[i]
		}
	}
	return nil
}

func setLiteralEnv(container *corev1.Container, name, value string) {
	for i := range container.Env {
		if container.Env[i].Name == name {
			container.Env[i] = corev1.EnvVar{Name: name, Value: value}
			return
		}
	}
	container.Env = append(container.Env, corev1.EnvVar{Name: name, Value: value})
}

func podMatchesSnapshot(
	ctx context.Context,
	reader client.Reader,
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
	rs := new(appsv1.ReplicaSet)
	if err := reader.Get(ctx, client.ObjectKey{Namespace: pod.Namespace, Name: owner.Name}, rs); err != nil {
		return false, fmt.Errorf("resolve ReplicaSet owner %s/%s: %w", pod.Namespace, owner.Name, err)
	}
	parent := metav1.GetControllerOf(rs)
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
