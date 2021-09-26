package mutator

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	admission "k8s.io/api/admission/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"

	"github.com/datawire/ambassador/v2/pkg/kates"
	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/managerutil"
	"github.com/telepresenceio/telepresence/v2/pkg/install"
)

const serviceAccountMountPath = "/var/run/secrets/kubernetes.io/serviceaccount"

func TestTrafficAgentInjector(t *testing.T) {
	env := &managerutil.Env{
		User:        "",
		ServerHost:  "tel-example",
		ServerPort:  "80",
		SystemAHost: "",
		SystemAPort: "",

		ManagerNamespace: "default",
		AgentRegistry:    "docker.io/datawire",
		AgentImage:       "tel2:2.3.1",
		AgentPort:        9900,
	}
	ctx := dlog.NewTestContext(t, false)
	ctx = managerutil.WithEnv(ctx, env)

	defaultSvc := &kates.Service{
		TypeMeta: metav1.TypeMeta{
			Kind:       "Service",
			APIVersion: "v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:        "some-name",
			Namespace:   "some-ns",
			Labels:      nil,
			Annotations: nil,
		},
		Spec: corev1.ServiceSpec{
			Ports: []corev1.ServicePort{{
				Name:       "proxied",
				Protocol:   "TCP",
				Port:       80,
				TargetPort: intstr.FromString("http"),
			}},
			Selector: map[string]string{
				"service": "some-name",
			},
		},
	}
	numericPortSvc := &kates.Service{
		TypeMeta: metav1.TypeMeta{
			Kind:       "Service",
			APIVersion: "v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:        "some-name",
			Namespace:   "some-ns",
			Labels:      nil,
			Annotations: nil,
		},
		Spec: corev1.ServiceSpec{
			Ports: []corev1.ServicePort{{
				Protocol:   "TCP",
				Port:       80,
				TargetPort: intstr.FromInt(8888),
			}},
			Selector: map[string]string{
				"service": "some-name",
			},
		},
	}

	tests := []struct {
		name          string
		request       *admission.AdmissionRequest
		expectedPatch string
		expectedError string
		service       *kates.Service
	}{
		{
			"Skip Precondition: Not the right type of resource",
			toAdmissionRequest(metav1.GroupVersionResource{Resource: "IgnoredResourceType"}, corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{
					install.InjectAnnotation: "enabled",
				}, Namespace: "some-ns", Name: "some-name"},
			}),
			"",
			"",
			defaultSvc,
		},
		{
			"Error Precondition: Fail to unmarshall",
			toAdmissionRequest(podResource, "I'm a string value, not an object"),
			"",
			"could not deserialize pod object",
			defaultSvc,
		},
		{
			"Skip Precondition: No annotation",
			toAdmissionRequest(podResource, corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{Namespace: "some-ns", Name: "some-name"},
			}),
			"",
			"",
			defaultSvc,
		},
		{
			"Skip Precondition: No name/namespace",
			toAdmissionRequest(podResource, corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{
					install.InjectAnnotation: "enabled",
				}},
			}),
			"",
			"",
			defaultSvc,
		},
		{
			"Skip Precondition: Sidecar already injected",
			toAdmissionRequest(podResource, corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{
					install.InjectAnnotation: "enabled",
				}, Namespace: "some-ns", Name: "some-name"},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Ports: []corev1.ContainerPort{
								{Name: "tm-http", ContainerPort: 8888},
							},
						},
						{
							Name: install.AgentContainerName,
							Ports: []corev1.ContainerPort{
								{Name: "http", ContainerPort: 9900},
							},
						},
					},
					Volumes: []corev1.Volume{
						{
							Name: install.AgentAnnotationVolumeName,
						},
					},
				},
			}),
			"",
			"",
			defaultSvc,
		},
		{
			"Skip Precondition: No port specified",
			toAdmissionRequest(podResource, corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{
					install.InjectAnnotation: "enabled",
				}, Namespace: "some-ns", Name: "some-name"},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{Ports: []corev1.ContainerPort{}},
					},
				},
			}),
			"",
			"",
			defaultSvc,
		},
		{
			"Skip Precondition: Sidecar has port collision",
			toAdmissionRequest(podResource, corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						install.InjectAnnotation: "enabled",
					},
					Labels: map[string]string{
						"serivce": "some-name",
					},
					Namespace: "some-ns",
					Name:      "some-name"},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{Ports: []corev1.ContainerPort{
							{Name: "http", ContainerPort: env.AgentPort},
						}},
					},
				},
			}),
			"",
			"",
			defaultSvc,
		},
		{
			"Apply Patch: Named port",
			toAdmissionRequest(podResource, corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						install.InjectAnnotation: "enabled",
					},
					Labels: map[string]string{
						"service": "some-name",
					},
					Namespace: "some-ns",
					Name:      "some-name"},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{
						Name:  "some-app-name",
						Image: "some-app-image",
						Ports: []corev1.ContainerPort{{
							Name: "http", ContainerPort: 8888},
						}},
					},
				},
			}),
			`[` +
				`{"op":"add","path":"/spec/containers/-","value":{` +
				`"name":"traffic-agent",` +
				`"image":"docker.io/datawire/tel2:2.3.1",` +
				`"args":["agent"],` +
				`"ports":[{"name":"http","containerPort":9900,"protocol":"TCP"}],` +
				`"env":[` +
				`{"name":"TELEPRESENCE_CONTAINER","value":"some-app-name"},` +
				`{"name":"_TEL_AGENT_LOG_LEVEL","value":"info"},` +
				`{"name":"_TEL_AGENT_NAME","value":"some-name"},` +
				`{"name":"_TEL_AGENT_NAMESPACE","valueFrom":{"fieldRef":{"fieldPath":"metadata.namespace"}}},` +
				`{"name":"_TEL_AGENT_POD_IP","valueFrom":{"fieldRef":{"fieldPath":"status.podIP"}}},` +
				`{"name":"_TEL_AGENT_APP_PORT","value":"8888"},` +
				`{"name":"_TEL_AGENT_MANAGER_HOST","value":"traffic-manager.default"}` +
				`],` +
				`"resources":{},` +
				`"volumeMounts":[{"name":"traffic-annotations","mountPath":"/tel_pod_info"}],` +
				`"readinessProbe":{"exec":{"command":["/bin/stat","/tmp/agent/ready"]}},` +
				`"securityContext":{"runAsUser":7777,"runAsGroup":7777,"runAsNonRoot":true}` +
				`}},` +
				`{"op":"replace","path":"/spec/containers/0/ports/0/name","value":"tm-http"},` +
				`{"op":"add","path":"/spec/volumes/-","value":{` +
				`"name":"traffic-annotations",` +
				`"downwardAPI":{"items":[{"path":"annotations","fieldRef":{"fieldPath":"metadata.annotations"}}]}` +
				`}}` +
				`]`,
			"",
			defaultSvc,
		},
		{
			"Apply Patch: Numeric port",
			toAdmissionRequest(podResource, corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						install.InjectAnnotation: "enabled",
					},
					Labels: map[string]string{
						"service": "some-name",
					},
					Namespace: "some-ns",
					Name:      "some-name"},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{
						Name:  "some-app-name",
						Image: "some-app-image",
						Ports: []corev1.ContainerPort{{ContainerPort: 8888}}},
					},
				},
			}),
			`[` +
				`{"op":"add","path":"/spec/containers/-","value":{` +
				`"name":"traffic-agent",` +
				`"image":"docker.io/datawire/tel2:2.3.1",` +
				`"args":["agent"],` +
				`"ports":[{"containerPort":9900,"protocol":"TCP"}],` +
				`"env":[` +
				`{"name":"TELEPRESENCE_CONTAINER","value":"some-app-name"},` +
				`{"name":"_TEL_AGENT_LOG_LEVEL","value":"info"},` +
				`{"name":"_TEL_AGENT_NAME","value":"some-name"},` +
				`{"name":"_TEL_AGENT_NAMESPACE","valueFrom":{"fieldRef":{"fieldPath":"metadata.namespace"}}},` +
				`{"name":"_TEL_AGENT_POD_IP","valueFrom":{"fieldRef":{"fieldPath":"status.podIP"}}},` +
				`{"name":"_TEL_AGENT_APP_PORT","value":"8888"},` +
				`{"name":"_TEL_AGENT_MANAGER_HOST","value":"traffic-manager.default"}` +
				`],` +
				`"resources":{},` +
				`"volumeMounts":[{"name":"traffic-annotations","mountPath":"/tel_pod_info"}],` +
				`"readinessProbe":{"exec":{"command":["/bin/stat","/tmp/agent/ready"]}},` +
				`"securityContext":{"runAsUser":7777,"runAsGroup":7777,"runAsNonRoot":true}` +
				`}},` +
				`{"op":"add","path":"/spec/initContainers","value":[]},` +
				`{"op":"add","path":"/spec/initContainers/-","value":{` +
				`"name":"tel-agent-init",` +
				`"image":"docker.io/datawire/tel2:2.3.1",` +
				`"args":["agent-init"],` +
				`"env":[` +
				`{"name":"APP_PORT","value":"8888"},` +
				`{"name":"AGENT_PORT","value":"9900"},` +
				`{"name":"AGENT_PROTOCOL","value":"TCP"}` +
				`],` +
				`"resources":{},` +
				`"securityContext":{"capabilities":{"add":["NET_ADMIN"]}}` +
				`}},` +
				`{"op":"add","path":"/spec/volumes/-","value":{` +
				`"name":"traffic-annotations",` +
				`"downwardAPI":{"items":[{"path":"annotations","fieldRef":{"fieldPath":"metadata.annotations"}}]}` +
				`}}` +
				`]`,
			"",
			numericPortSvc,
		},
		{
			"Apply Patch: Numeric port with init containers",
			toAdmissionRequest(podResource, corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						install.InjectAnnotation: "enabled",
					},
					Labels: map[string]string{
						"service": "some-name",
					},
					Namespace: "some-ns",
					Name:      "some-name"},
				Spec: corev1.PodSpec{
					InitContainers: []corev1.Container{{
						Name:  "some-init-container",
						Image: "some-init-image",
					}},
					Containers: []corev1.Container{{
						Name:  "some-app-name",
						Image: "some-app-image",
						Ports: []corev1.ContainerPort{{ContainerPort: 8888}}},
					},
				},
			}),
			`[` +
				`{"op":"add","path":"/spec/containers/-","value":{` +
				`"name":"traffic-agent",` +
				`"image":"docker.io/datawire/tel2:2.3.1",` +
				`"args":["agent"],` +
				`"ports":[{"containerPort":9900,"protocol":"TCP"}],` +
				`"env":[` +
				`{"name":"TELEPRESENCE_CONTAINER","value":"some-app-name"},` +
				`{"name":"_TEL_AGENT_LOG_LEVEL","value":"info"},` +
				`{"name":"_TEL_AGENT_NAME","value":"some-name"},` +
				`{"name":"_TEL_AGENT_NAMESPACE","valueFrom":{"fieldRef":{"fieldPath":"metadata.namespace"}}},` +
				`{"name":"_TEL_AGENT_POD_IP","valueFrom":{"fieldRef":{"fieldPath":"status.podIP"}}},` +
				`{"name":"_TEL_AGENT_APP_PORT","value":"8888"},` +
				`{"name":"_TEL_AGENT_MANAGER_HOST","value":"traffic-manager.default"}` +
				`],` +
				`"resources":{},` +
				`"volumeMounts":[{"name":"traffic-annotations","mountPath":"/tel_pod_info"}],` +
				`"readinessProbe":{"exec":{"command":["/bin/stat","/tmp/agent/ready"]}},` +
				`"securityContext":{"runAsUser":7777,"runAsGroup":7777,"runAsNonRoot":true}` +
				`}},` +
				`{"op":"add","path":"/spec/initContainers/-","value":{` +
				`"name":"tel-agent-init",` +
				`"image":"docker.io/datawire/tel2:2.3.1",` +
				`"args":["agent-init"],` +
				`"env":[` +
				`{"name":"APP_PORT","value":"8888"},` +
				`{"name":"AGENT_PORT","value":"9900"},` +
				`{"name":"AGENT_PROTOCOL","value":"TCP"}` +
				`],` +
				`"resources":{},` +
				`"securityContext":{"capabilities":{"add":["NET_ADMIN"]}}` +
				`}},` +
				`{"op":"add","path":"/spec/volumes/-","value":{` +
				`"name":"traffic-annotations",` +
				`"downwardAPI":{"items":[{"path":"annotations","fieldRef":{"fieldPath":"metadata.annotations"}}]}` +
				`}}` +
				`]`,
			"",
			numericPortSvc,
		},
		{
			"Apply Patch: Numeric port re-processing",
			toAdmissionRequest(podResource, corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						install.InjectAnnotation: "enabled",
					},
					Labels: map[string]string{
						"service": "some-name",
					},
					Namespace: "some-ns",
					Name:      "some-name"},
				Spec: corev1.PodSpec{
					InitContainers: []corev1.Container{
						{
							Name: install.InitContainerName,
						},
						{
							Name:  "some-init-container",
							Image: "some-init-image",
						},
					},
					Containers: []corev1.Container{
						{
							Name:  "some-app-name",
							Image: "some-app-image",
							Ports: []corev1.ContainerPort{{ContainerPort: 8888}},
						},
						{
							Name:  install.AgentContainerName,
							Ports: []corev1.ContainerPort{{ContainerPort: 9900}},
						},
					},
					Volumes: []corev1.Volume{{
						Name: install.AgentAnnotationVolumeName,
					}},
				},
			}),
			`[` +
				`{"op":"remove","path":"/spec/initContainers/0"},` +
				`{"op":"add","path":"/spec/initContainers/-","value":{` +
				`"name":"tel-agent-init",` +
				`"image":"docker.io/datawire/tel2:2.3.1",` +
				`"args":["agent-init"],` +
				`"env":[` +
				`{"name":"APP_PORT","value":"8888"},` +
				`{"name":"AGENT_PORT","value":"9900"},` +
				`{"name":"AGENT_PROTOCOL","value":"TCP"}` +
				`],` +
				`"resources":{},` +
				`"securityContext":{"capabilities":{"add":["NET_ADMIN"]}}` +
				`}}` +
				`]`,
			"",
			numericPortSvc,
		},
		{
			"Apply Patch: volumes are copied",
			toAdmissionRequest(podResource, corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						install.InjectAnnotation: "enabled",
					},
					Labels: map[string]string{
						"service": "some-name",
					},
					Namespace: "some-ns",
					Name:      "some-name"},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{
						Name:  "some-app-name",
						Image: "some-app-image",
						Ports: []corev1.ContainerPort{{
							Name: "http", ContainerPort: 8888},
						},
						VolumeMounts: []corev1.VolumeMount{
							{Name: "some-token", ReadOnly: true, MountPath: serviceAccountMountPath},
						}},
					},
				},
			}),
			`[{"op":"add","path":"/spec/containers/-","value":{` +
				`"name":"traffic-agent",` +
				`"image":"docker.io/datawire/tel2:2.3.1",` +
				`"args":["agent"],` +
				`"ports":[{"name":"http","containerPort":9900,"protocol":"TCP"}],` +
				`"env":[` +
				`{"name":"TELEPRESENCE_CONTAINER","value":"some-app-name"},` +
				`{"name":"_TEL_AGENT_LOG_LEVEL","value":"info"},` +
				`{"name":"_TEL_AGENT_NAME","value":"some-name"},` +
				`{"name":"_TEL_AGENT_NAMESPACE","valueFrom":{"fieldRef":{"fieldPath":"metadata.namespace"}}},` +
				`{"name":"_TEL_AGENT_POD_IP","valueFrom":{"fieldRef":{"fieldPath":"status.podIP"}}},` +
				`{"name":"_TEL_AGENT_APP_PORT","value":"8888"},` +
				`{"name":"_TEL_AGENT_APP_MOUNTS","value":"/tel_app_mounts"},` +
				`{"name":"TELEPRESENCE_MOUNTS","value":"/var/run/secrets/kubernetes.io/serviceaccount"},` +
				`{"name":"_TEL_AGENT_MANAGER_HOST","value":"traffic-manager.default"}` +
				`],` +
				`"resources":{},` +
				`"volumeMounts":[` +
				`{"name":"some-token","readOnly":true,"mountPath":"/var/run/secrets/kubernetes.io/serviceaccount"},` +
				`{"name":"traffic-annotations","mountPath":"/tel_pod_info"}` +
				`],` +
				`"readinessProbe":{"exec":{"command":["/bin/stat","/tmp/agent/ready"]}},` +
				`"securityContext":{"runAsUser":7777,"runAsGroup":7777,"runAsNonRoot":true}` +
				`}},` +
				`{"op":"replace","path":"/spec/containers/0/ports/0/name","value":"tm-http"},` +
				`{"op":"add","path":"/spec/volumes/-","value":{` +
				`"name":"traffic-annotations",` +
				`"downwardAPI":{"items":[{"path":"annotations","fieldRef":{"fieldPath":"metadata.annotations"}}]}` +
				`}}` +
				`]`,
			"",
			defaultSvc,
		},
	}

	for _, test := range tests {
		test := test // pin it
		t.Run(test.name, func(t *testing.T) {
			fms := findMatchingService
			defer func() {
				findMatchingService = fms
			}()
			findMatchingService = func(c context.Context, client *kates.Client, portNameOrNumber, svcName, namespace string, labels map[string]string) (*kates.Service, error) {
				return test.service, nil
			}

			actualPatch, actualErr := agentInjector(ctx, test.request)
			assertContains(t, actualErr, test.expectedError)
			if actualPatch != nil || test.expectedPatch != "" {
				patchBytes, err := json.Marshal(actualPatch)
				require.NoError(t, err)
				patchString := string(patchBytes)
				assert.Equal(t, test.expectedPatch, patchString, "patches differ")
			}
		})
	}
}

func assertContains(t *testing.T, err error, expected string) {
	if expected == "" {
		assert.NoError(t, err)
		return
	}
	if err == nil {
		assert.Emptyf(t, expected, "expected error %q", expected)
		return
	}
	assert.Contains(t, err.Error(), expected)
}

func toAdmissionRequest(resource metav1.GroupVersionResource, object interface{}) *admission.AdmissionRequest {
	bytes, _ := json.Marshal(object)
	return &admission.AdmissionRequest{
		Resource:  resource,
		Object:    runtime.RawExtension{Raw: bytes},
		Namespace: "default",
	}
}
