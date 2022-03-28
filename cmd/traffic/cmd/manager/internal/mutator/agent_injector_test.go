package mutator

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	admission "k8s.io/api/admission/v1"
	core "k8s.io/api/core/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/managerutil"
	"github.com/telepresenceio/telepresence/v2/pkg/install"
)

const serviceAccountMountPath = "/var/run/secrets/kubernetes.io/serviceaccount"

func TestTrafficAgentInjector(t *testing.T) {
	type svcFinder func(c context.Context, portNameOrNumber, svcName, namespace string, labels map[string]string) (*core.Service, error)
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

	defaultSvc := &core.Service{
		TypeMeta: meta.TypeMeta{
			Kind:       "Service",
			APIVersion: "v1",
		},
		ObjectMeta: meta.ObjectMeta{
			Name:        "some-name",
			Namespace:   "some-ns",
			Labels:      nil,
			Annotations: nil,
		},
		Spec: core.ServiceSpec{
			Ports: []core.ServicePort{{
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
	defaultSvcFinder := func(c context.Context, portNameOrNumber, svcName, namespace string, labels map[string]string) (*core.Service, error) {
		return defaultSvc, nil
	}
	numericPortSvcFinder := func(c context.Context, portNameOrNumber, svcName, namespace string, labels map[string]string) (*core.Service, error) {
		return &core.Service{
			TypeMeta: meta.TypeMeta{
				Kind:       "Service",
				APIVersion: "v1",
			},
			ObjectMeta: meta.ObjectMeta{
				Name:        "some-name",
				Namespace:   "some-ns",
				Labels:      nil,
				Annotations: nil,
			},
			Spec: core.ServiceSpec{
				Ports: []core.ServicePort{{
					Protocol:   "TCP",
					Port:       80,
					TargetPort: intstr.FromInt(8888),
				}},
				Selector: map[string]string{
					"service": "some-name",
				},
			},
		}, nil
	}
	multiSvcFinder := func(c context.Context, portNameOrNumber, svcName, namespace string, labels map[string]string) (*core.Service, error) {
		// simulate not being given a service name and finding multiple services
		if svcName == "" {
			return nil, fmt.Errorf("multiple services found")
		}
		if svcName == defaultSvc.Name {
			return defaultSvc, nil
		}
		return nil, fmt.Errorf("no services found")
	}

	tests := []struct {
		name          string
		request       *admission.AdmissionRequest
		expectedPatch string
		expectedError string
		serviceFinder svcFinder
		envAdditions  *managerutil.Env
	}{
		{
			"Skip Precondition: Not the right type of resource",
			toAdmissionRequest(meta.GroupVersionResource{Resource: "IgnoredResourceType"}, core.Pod{
				ObjectMeta: meta.ObjectMeta{Annotations: map[string]string{
					install.InjectAnnotation: "enabled",
				}, Namespace: "some-ns", Name: "some-name"},
			}),
			"",
			"",
			defaultSvcFinder,
			nil,
		},
		{
			"Error Precondition: Fail to unmarshall",
			toAdmissionRequest(podResource, "I'm a string value, not an object"),
			"",
			"could not deserialize pod object",
			defaultSvcFinder,
			nil,
		},
		{
			"Skip Precondition: No annotation",
			toAdmissionRequest(podResource, core.Pod{
				ObjectMeta: meta.ObjectMeta{Namespace: "some-ns", Name: "some-name"},
			}),
			"",
			"",
			defaultSvcFinder,
			nil,
		},
		{
			"Skip Precondition: No name/namespace",
			toAdmissionRequest(podResource, core.Pod{
				ObjectMeta: meta.ObjectMeta{Annotations: map[string]string{
					install.InjectAnnotation: "enabled",
				}},
			}),
			"",
			"",
			defaultSvcFinder,
			nil,
		},
		{
			"Skip Precondition: Sidecar already injected",
			toAdmissionRequest(podResource, core.Pod{
				ObjectMeta: meta.ObjectMeta{Annotations: map[string]string{
					install.InjectAnnotation: "enabled",
				}, Namespace: "some-ns", Name: "some-name"},
				Spec: core.PodSpec{
					Containers: []core.Container{
						{
							Ports: []core.ContainerPort{
								{Name: "tm-http", ContainerPort: 8888},
							},
						},
						{
							Name: install.AgentContainerName,
							Ports: []core.ContainerPort{
								{Name: "http", ContainerPort: 9900},
							},
						},
					},
					Volumes: []core.Volume{
						{
							Name: install.AgentAnnotationVolumeName,
						},
					},
				},
			}),
			"",
			"",
			defaultSvcFinder,
			nil,
		},
		{
			"Error Precondition: No port specified",
			toAdmissionRequest(podResource, core.Pod{
				ObjectMeta: meta.ObjectMeta{Annotations: map[string]string{
					install.InjectAnnotation: "enabled",
				}, Namespace: "some-ns", Name: "some-name"},
				Spec: core.PodSpec{
					Containers: []core.Container{
						{Ports: []core.ContainerPort{}},
					},
				},
			}),
			"",
			"found no Service with a port that matches any container in this workload",
			defaultSvcFinder,
			nil,
		},
		{
			"Error Precondition: Sidecar has port collision",
			toAdmissionRequest(podResource, core.Pod{
				ObjectMeta: meta.ObjectMeta{
					Annotations: map[string]string{
						install.InjectAnnotation: "enabled",
					},
					Labels: map[string]string{
						"service": "some-name",
					},
					Namespace: "some-ns",
					Name:      "some-name"},
				Spec: core.PodSpec{
					Containers: []core.Container{
						{Ports: []core.ContainerPort{
							{Name: "http", ContainerPort: env.AgentPort},
						}},
					},
				},
			}),
			"",
			"is exposing the same port (9900) as the traffic-agent sidecar",
			defaultSvcFinder,
			nil,
		},
		{
			"Apply Patch: Named port",
			toAdmissionRequest(podResource, core.Pod{
				ObjectMeta: meta.ObjectMeta{
					Annotations: map[string]string{
						install.InjectAnnotation: "enabled",
					},
					Labels: map[string]string{
						"service": "some-name",
					},
					Namespace: "some-ns",
					Name:      "some-name"},
				Spec: core.PodSpec{
					Containers: []core.Container{{
						Name:  "some-app-name",
						Image: "some-app-image",
						Ports: []core.ContainerPort{{
							Name: "http", ContainerPort: 8888},
						}},
					},
				},
			}),
			`[` +
				`{"op":"replace","path":"/spec/containers/0/ports/0/name","value":"tm-http"},` +
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
				`{"name":"_TEL_AGENT_PORT","value":"9900"},` +
				`{"name":"_TEL_AGENT_MANAGER_HOST","value":"traffic-manager.default"}` +
				`],` +
				`"resources":{},` +
				`"volumeMounts":[{"name":"traffic-annotations","mountPath":"/tel_pod_info"}],` +
				`"readinessProbe":{"exec":{"command":["/bin/stat","/tmp/agent/ready"]}}` +
				`}},` +
				`{"op":"add","path":"/spec/volumes/-","value":{` +
				`"name":"traffic-annotations",` +
				`"downwardAPI":{"items":[{"path":"annotations","fieldRef":{"fieldPath":"metadata.annotations"}}]}` +
				`}}` +
				`]`,
			"",
			defaultSvcFinder,
			nil,
		},
		{
			"Apply Patch: Telepresence API Port",
			toAdmissionRequest(podResource, core.Pod{
				ObjectMeta: meta.ObjectMeta{
					Annotations: map[string]string{
						install.InjectAnnotation: "enabled",
					},
					Labels: map[string]string{
						"service": "some-name",
					},
					Namespace: "some-ns",
					Name:      "some-name"},
				Spec: core.PodSpec{
					Containers: []core.Container{{
						Name:  "some-app-name",
						Image: "some-app-image",
						Ports: []core.ContainerPort{{
							Name: "http", ContainerPort: 8888},
						}},
					},
				},
			}),
			`[` +
				`{"op":"replace","path":"/spec/containers/0/ports/0/name","value":"tm-http"},` +
				`{"op":"replace","path":"/spec/containers/0/env","value":[]},{"op":"add","path":"/spec/containers/0/env/-","value":{"name":"TELEPRESENCE_API_PORT","value":"9981"}},` +
				`{"op":"add","path":"/spec/containers/-","value":{` +
				`"name":"traffic-agent",` +
				`"image":"docker.io/datawire/tel2:2.3.1",` +
				`"args":["agent"],` +
				`"ports":[{"name":"http","containerPort":9900,"protocol":"TCP"}],` +
				`"env":[` +
				`{"name":"TELEPRESENCE_API_PORT","value":"9981"},` +
				`{"name":"TELEPRESENCE_CONTAINER","value":"some-app-name"},` +
				`{"name":"_TEL_AGENT_LOG_LEVEL","value":"info"},` +
				`{"name":"_TEL_AGENT_NAME","value":"some-name"},` +
				`{"name":"_TEL_AGENT_NAMESPACE","valueFrom":{"fieldRef":{"fieldPath":"metadata.namespace"}}},` +
				`{"name":"_TEL_AGENT_POD_IP","valueFrom":{"fieldRef":{"fieldPath":"status.podIP"}}},` +
				`{"name":"_TEL_AGENT_APP_PORT","value":"8888"},` +
				`{"name":"_TEL_AGENT_PORT","value":"9900"},` +
				`{"name":"_TEL_AGENT_MANAGER_HOST","value":"traffic-manager.default"}` +
				`],` +
				`"resources":{},` +
				`"volumeMounts":[{"name":"traffic-annotations","mountPath":"/tel_pod_info"}],` +
				`"readinessProbe":{"exec":{"command":["/bin/stat","/tmp/agent/ready"]}}` +
				`}},` +
				`{"op":"add","path":"/spec/volumes/-","value":{` +
				`"name":"traffic-annotations",` +
				`"downwardAPI":{"items":[{"path":"annotations","fieldRef":{"fieldPath":"metadata.annotations"}}]}` +
				`}}` +
				`]`,
			"",
			defaultSvcFinder,
			&managerutil.Env{
				APIPort: 9981,
			},
		},
		{
			"Error Precondition: Multiple services",
			toAdmissionRequest(podResource, core.Pod{
				ObjectMeta: meta.ObjectMeta{
					Annotations: map[string]string{
						install.InjectAnnotation: "enabled",
					},
					Labels: map[string]string{
						"service": "some-name",
					},
					Namespace: "some-ns",
					Name:      "some-name"},
				Spec: core.PodSpec{
					Containers: []core.Container{{
						Name:  "some-app-name",
						Image: "some-app-image",
						Ports: []core.ContainerPort{{
							Name: "http", ContainerPort: 8888},
						}},
					},
				},
			}),
			"",
			"multiple services found",
			multiSvcFinder,
			nil,
		},
		{
			"Error Precondition: Invalid service name",
			toAdmissionRequest(podResource, core.Pod{
				ObjectMeta: meta.ObjectMeta{
					Annotations: map[string]string{
						install.InjectAnnotation:      "enabled",
						install.ServiceNameAnnotation: "khruangbin",
					},
					Labels: map[string]string{
						"service": "some-name",
					},
					Namespace: "some-ns",
					Name:      "some-name"},
				Spec: core.PodSpec{
					Containers: []core.Container{{
						Name:  "some-app-name",
						Image: "some-app-image",
						Ports: []core.ContainerPort{{
							Name: "http", ContainerPort: 8888},
						}},
					},
				},
			}),
			"",
			"no services found",
			multiSvcFinder,
			nil,
		},
		{
			"Apply Patch: Multiple services",
			toAdmissionRequest(podResource, core.Pod{
				ObjectMeta: meta.ObjectMeta{
					Annotations: map[string]string{
						install.InjectAnnotation:      "enabled",
						install.ServiceNameAnnotation: defaultSvc.Name,
					},
					Labels: map[string]string{
						"service": "some-name",
					},
					Namespace: "some-ns",
					Name:      "some-name"},
				Spec: core.PodSpec{
					Containers: []core.Container{{
						Name:  "some-app-name",
						Image: "some-app-image",
						Ports: []core.ContainerPort{{
							Name: "http", ContainerPort: 8888},
						}},
					},
				},
			}),
			`[` +
				`{"op":"replace","path":"/spec/containers/0/ports/0/name","value":"tm-http"},` +
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
				`{"name":"_TEL_AGENT_PORT","value":"9900"},` +
				`{"name":"_TEL_AGENT_MANAGER_HOST","value":"traffic-manager.default"}` +
				`],` +
				`"resources":{},` +
				`"volumeMounts":[{"name":"traffic-annotations","mountPath":"/tel_pod_info"}],` +
				`"readinessProbe":{"exec":{"command":["/bin/stat","/tmp/agent/ready"]}}` +
				`}},` +
				`{"op":"add","path":"/spec/volumes/-","value":{` +
				`"name":"traffic-annotations",` +
				`"downwardAPI":{"items":[{"path":"annotations","fieldRef":{"fieldPath":"metadata.annotations"}}]}` +
				`}}` +
				`]`,
			"",
			multiSvcFinder,
			nil,
		},
		{
			"Apply Patch: Numeric port",
			toAdmissionRequest(podResource, core.Pod{
				ObjectMeta: meta.ObjectMeta{
					Annotations: map[string]string{
						install.InjectAnnotation: "enabled",
					},
					Labels: map[string]string{
						"service": "some-name",
					},
					Namespace: "some-ns",
					Name:      "some-name"},
				Spec: core.PodSpec{
					Containers: []core.Container{{
						Name:  "some-app-name",
						Image: "some-app-image",
						Ports: []core.ContainerPort{{ContainerPort: 8888}}},
					},
				},
			}),
			`[` +
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
				`{"name":"_TEL_AGENT_PORT","value":"9900"},` +
				`{"name":"_TEL_AGENT_MANAGER_HOST","value":"traffic-manager.default"}` +
				`],` +
				`"resources":{},` +
				`"volumeMounts":[{"name":"traffic-annotations","mountPath":"/tel_pod_info"}],` +
				`"readinessProbe":{"exec":{"command":["/bin/stat","/tmp/agent/ready"]}}` +
				`}},` +
				`{"op":"add","path":"/spec/volumes/-","value":{` +
				`"name":"traffic-annotations",` +
				`"downwardAPI":{"items":[{"path":"annotations","fieldRef":{"fieldPath":"metadata.annotations"}}]}` +
				`}}` +
				`]`,
			"",
			numericPortSvcFinder,
			nil,
		},
		{
			"Apply Patch: Numeric port with init containers",
			toAdmissionRequest(podResource, core.Pod{
				ObjectMeta: meta.ObjectMeta{
					Annotations: map[string]string{
						install.InjectAnnotation: "enabled",
					},
					Labels: map[string]string{
						"service": "some-name",
					},
					Namespace: "some-ns",
					Name:      "some-name"},
				Spec: core.PodSpec{
					InitContainers: []core.Container{{
						Name:  "some-init-container",
						Image: "some-init-image",
					}},
					Containers: []core.Container{{
						Name:  "some-app-name",
						Image: "some-app-image",
						Ports: []core.ContainerPort{{ContainerPort: 8888}}},
					},
				},
			}),
			`[` +
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
				`{"name":"_TEL_AGENT_PORT","value":"9900"},` +
				`{"name":"_TEL_AGENT_MANAGER_HOST","value":"traffic-manager.default"}` +
				`],` +
				`"resources":{},` +
				`"volumeMounts":[{"name":"traffic-annotations","mountPath":"/tel_pod_info"}],` +
				`"readinessProbe":{"exec":{"command":["/bin/stat","/tmp/agent/ready"]}}` +
				`}},` +
				`{"op":"add","path":"/spec/volumes/-","value":{` +
				`"name":"traffic-annotations",` +
				`"downwardAPI":{"items":[{"path":"annotations","fieldRef":{"fieldPath":"metadata.annotations"}}]}` +
				`}}` +
				`]`,
			"",
			numericPortSvcFinder,
			nil,
		},
		{
			"Apply Patch: Numeric port re-processing",
			toAdmissionRequest(podResource, core.Pod{
				ObjectMeta: meta.ObjectMeta{
					Annotations: map[string]string{
						install.InjectAnnotation: "enabled",
					},
					Labels: map[string]string{
						"service": "some-name",
					},
					Namespace: "some-ns",
					Name:      "some-name"},
				Spec: core.PodSpec{
					InitContainers: []core.Container{
						{
							Name: install.InitContainerName,
						},
						{
							Name:  "some-init-container",
							Image: "some-init-image",
						},
					},
					Containers: []core.Container{
						{
							Name:  "some-app-name",
							Image: "some-app-image",
							Ports: []core.ContainerPort{{ContainerPort: 8888}},
						},
						{
							Name:  install.AgentContainerName,
							Ports: []core.ContainerPort{{ContainerPort: 9900}},
						},
					},
					Volumes: []core.Volume{{
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
			numericPortSvcFinder,
			nil,
		},
		{
			"Apply Patch: volumes are copied",
			toAdmissionRequest(podResource, core.Pod{
				ObjectMeta: meta.ObjectMeta{
					Annotations: map[string]string{
						install.InjectAnnotation: "enabled",
					},
					Labels: map[string]string{
						"service": "some-name",
					},
					Namespace: "some-ns",
					Name:      "some-name"},
				Spec: core.PodSpec{
					Containers: []core.Container{{
						Name:  "some-app-name",
						Image: "some-app-image",
						Ports: []core.ContainerPort{{
							Name: "http", ContainerPort: 8888},
						},
						VolumeMounts: []core.VolumeMount{
							{Name: "some-token", ReadOnly: true, MountPath: serviceAccountMountPath},
						}},
					},
				},
			}),
			`[{"op":"replace","path":"/spec/containers/0/ports/0/name","value":"tm-http"},` +
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
				`{"name":"_TEL_AGENT_PORT","value":"9900"},` +
				`{"name":"_TEL_AGENT_APP_MOUNTS","value":"/tel_app_mounts"},` +
				`{"name":"TELEPRESENCE_MOUNTS","value":"/var/run/secrets/kubernetes.io/serviceaccount"},` +
				`{"name":"_TEL_AGENT_MANAGER_HOST","value":"traffic-manager.default"}` +
				`],` +
				`"resources":{},` +
				`"volumeMounts":[` +
				`{"name":"some-token","readOnly":true,"mountPath":"/var/run/secrets/kubernetes.io/serviceaccount"},` +
				`{"name":"traffic-annotations","mountPath":"/tel_pod_info"}` +
				`],` +
				`"readinessProbe":{"exec":{"command":["/bin/stat","/tmp/agent/ready"]}}` +
				`}},` +
				`{"op":"add","path":"/spec/volumes/-","value":{` +
				`"name":"traffic-annotations",` +
				`"downwardAPI":{"items":[{"path":"annotations","fieldRef":{"fieldPath":"metadata.annotations"}}]}` +
				`}}` +
				`]`,
			"",
			defaultSvcFinder,
			nil,
		},
	}

	for _, test := range tests {
		test := test // pin it
		ctx := ctx
		t.Run(test.name, func(t *testing.T) {
			if test.envAdditions != nil {
				env := managerutil.GetEnv(ctx)
				newEnv := *env
				ne := reflect.ValueOf(&newEnv).Elem()
				ae := reflect.ValueOf(test.envAdditions).Elem()
				for i := ae.NumField() - 1; i >= 0; i-- {
					ef := ae.Field(i)
					if (ef.Kind() == reflect.String || ef.Kind() == reflect.Int32) && !ef.IsZero() {
						ne.Field(i).Set(ef)
					}
				}
				ctx = managerutil.WithEnv(ctx, &newEnv)
			}
			fms := findMatchingService
			defer func() {
				findMatchingService = fms
			}()
			findMatchingService = test.serviceFinder

			actualPatch, actualErr := agentInjector(ctx, test.request)
			requireContains(t, actualErr, test.expectedError)
			if actualPatch != nil || test.expectedPatch != "" {
				patchBytes, err := json.Marshal(actualPatch)
				require.NoError(t, err)
				patchString := string(patchBytes)
				assert.Equal(t, test.expectedPatch, patchString, "patches differ")
			}
		})
	}
}

func requireContains(t *testing.T, err error, expected string) {
	if expected == "" {
		require.NoError(t, err)
		return
	}
	if err == nil {
		require.Emptyf(t, expected, "expected error %q", expected)
		return
	}
	require.Contains(t, err.Error(), expected)
}

func toAdmissionRequest(resource meta.GroupVersionResource, object interface{}) *admission.AdmissionRequest {
	bytes, _ := json.Marshal(object)
	return &admission.AdmissionRequest{
		Resource:  resource,
		Object:    runtime.RawExtension{Raw: bytes},
		Namespace: "default",
	}
}
