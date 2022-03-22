package mutator

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	admission "k8s.io/api/admission/v1"
	apps "k8s.io/api/apps/v1"
	core "k8s.io/api/core/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes/fake"
	"sigs.k8s.io/yaml"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/internal/mutator/agentconfig"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/managerutil"
	"github.com/telepresenceio/telepresence/v2/pkg/install"
	"github.com/telepresenceio/telepresence/v2/pkg/install/agent"
	"github.com/telepresenceio/telepresence/v2/pkg/k8sapi"
)

const serviceAccountMountPath = "/var/run/secrets/kubernetes.io/serviceaccount"

func int32P(i int32) *int32 {
	return &i
}
func boolP(b bool) *bool {
	return &b
}
func stringP(s string) *string {
	return &s
}

func TestTrafficAgentConfigGenerator(t *testing.T) {
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

	podSuffix := "-6699c6cb54-"
	podName := func(name string) string {
		return name + podSuffix
	}

	wlName := func(podName string) string {
		return strings.TrimSuffix(podName, podSuffix)
	}

	podOwner := func(name string) []meta.OwnerReference {
		return []meta.OwnerReference{
			{
				APIVersion: "apps/v1",
				Kind:       "Deployment",
				Name:       name,
				Controller: boolP(true),
			},
		}
	}

	podObjectMeta := func(name, labelKey string) meta.ObjectMeta {
		return meta.ObjectMeta{
			Name:            podName(name),
			Namespace:       "some-ns",
			Annotations:     map[string]string{install.InjectAnnotation: "enabled"},
			Labels:          map[string]string{labelKey: name},
			OwnerReferences: podOwner(name),
		}
	}

	podNamedPort := core.Pod{
		ObjectMeta: podObjectMeta("named-port", "service"),
		Spec: core.PodSpec{
			Containers: []core.Container{
				{
					Name: "some-container",
					Ports: []core.ContainerPort{
						{
							Name: "http", ContainerPort: 8888,
						},
					},
					VolumeMounts: []core.VolumeMount{{
						Name:      "bob",
						MountPath: "/home/bob",
					}},
				},
			},
		},
	}

	podNumericPort := core.Pod{
		ObjectMeta: podObjectMeta("numeric-port", "app"),
		Spec: core.PodSpec{
			Containers: []core.Container{
				{
					Name: "some-container",
					Ports: []core.ContainerPort{
						{
							ContainerPort: 8899,
						},
					},
				},
			},
		},
	}

	podNamedAndNumericPort := core.Pod{
		ObjectMeta: meta.ObjectMeta{
			Name:            podName("named-and-numeric"),
			Namespace:       "some-ns",
			Annotations:     map[string]string{install.InjectAnnotation: "enabled"},
			Labels:          map[string]string{"service": "named-port", "app": "numeric-port"},
			OwnerReferences: podOwner("named-and-numeric"),
		},
		Spec: core.PodSpec{
			Containers: []core.Container{
				{
					Name: "named-port-container",
					Ports: []core.ContainerPort{
						{
							Name:          "http",
							ContainerPort: 8888,
						},
					},
					VolumeMounts: []core.VolumeMount{{
						Name:      "bob",
						MountPath: "/home/bob",
					}},
				},
				{
					Name: "numeric-port-container",
					Ports: []core.ContainerPort{
						{
							ContainerPort: 8899,
						},
					},
				},
			},
		},
	}

	podMultiPort := core.Pod{
		ObjectMeta: meta.ObjectMeta{
			Name:            podName("multi-port"),
			Namespace:       "some-ns",
			Annotations:     map[string]string{install.InjectAnnotation: "enabled"},
			Labels:          map[string]string{"service": "multi-port"},
			OwnerReferences: podOwner("multi-port"),
		},
		Spec: core.PodSpec{
			Containers: []core.Container{
				{
					Name: "multi-port-container",
					Ports: []core.ContainerPort{
						{
							Name:          "http",
							ContainerPort: 8080,
						},
						{
							Name:          "grpc",
							ContainerPort: 8081,
						},
					},
					VolumeMounts: []core.VolumeMount{{
						Name:      "bob",
						MountPath: "/home/bob",
					}},
				},
			},
		},
	}

	podMultiSplitPort := core.Pod{
		ObjectMeta: meta.ObjectMeta{
			Name:            podName("multi-container"),
			Namespace:       "some-ns",
			Annotations:     map[string]string{install.InjectAnnotation: "enabled"},
			Labels:          map[string]string{"service": "multi-port"},
			OwnerReferences: podOwner("multi-container"),
		},
		Spec: core.PodSpec{
			Containers: []core.Container{
				{
					Name: "http-container",
					Ports: []core.ContainerPort{
						{
							Name:          "http",
							ContainerPort: 8080,
						},
					},
					VolumeMounts: []core.VolumeMount{{
						Name:      "bob",
						MountPath: "/home/bob",
					}},
				},
				{
					Name: "grpc-container",
					Ports: []core.ContainerPort{
						{
							Name:          "grpc",
							ContainerPort: 8081,
						},
					},
				},
			},
		},
	}

	deployment := func(pod *core.Pod) *apps.Deployment {
		name := wlName(pod.Name)
		return &apps.Deployment{
			TypeMeta: meta.TypeMeta{
				Kind:       "Deployment",
				APIVersion: "apps/v1",
			},
			ObjectMeta: meta.ObjectMeta{
				Name:      name,
				Namespace: "some-ns",
				Labels:    pod.Labels,
			},
			Spec: apps.DeploymentSpec{
				Replicas: int32P(1),
				Template: core.PodTemplateSpec{
					ObjectMeta: pod.ObjectMeta,
					Spec:       pod.Spec,
				},
				Selector: &meta.LabelSelector{MatchLabels: pod.Labels},
			},
		}
	}

	makeUID := func() types.UID {
		uid, err := uuid.NewUUID()
		require.NoError(t, err)
		return types.UID(uid.String())
	}
	namedPortUID := makeUID()
	numericPortUID := makeUID()
	multiPortUID := makeUID()

	clientset := fake.NewSimpleClientset(
		&core.Service{
			TypeMeta: meta.TypeMeta{
				Kind:       "Service",
				APIVersion: "v1",
			},
			ObjectMeta: meta.ObjectMeta{
				Name:      "named-port",
				Namespace: "some-ns",
				UID:       namedPortUID,
			},
			Spec: core.ServiceSpec{
				Ports: []core.ServicePort{{
					Name:       "http",
					Protocol:   "TCP",
					Port:       80,
					TargetPort: intstr.FromString("http"),
				}},
				Selector: map[string]string{
					"service": "named-port",
				},
			},
		},
		&core.Service{
			TypeMeta: meta.TypeMeta{
				Kind:       "Service",
				APIVersion: "v1",
			},
			ObjectMeta: meta.ObjectMeta{
				Name:      "numeric-port",
				Namespace: "some-ns",
				UID:       numericPortUID,
			},
			Spec: core.ServiceSpec{
				Ports: []core.ServicePort{{
					Name:       "http",
					Protocol:   "TCP",
					Port:       80,
					TargetPort: intstr.FromInt(8899),
				}},
				Selector: map[string]string{
					"app": "numeric-port",
				},
			},
		},
		&core.Service{
			TypeMeta: meta.TypeMeta{
				Kind:       "Service",
				APIVersion: "v1",
			},
			ObjectMeta: meta.ObjectMeta{
				Name:      "multi-port",
				Namespace: "some-ns",
				UID:       multiPortUID,
			},
			Spec: core.ServiceSpec{
				Ports: []core.ServicePort{
					{
						Protocol:   "TCP",
						Name:       "http",
						Port:       80,
						TargetPort: intstr.FromString("http"),
					},
					{
						Protocol:    "TCP",
						Name:        "grpc",
						Port:        8001,
						AppProtocol: stringP("grpc"),
						TargetPort:  intstr.FromString("grpc"),
					}},
				Selector: map[string]string{
					"service": "multi-port",
				},
			},
		},
		&podNamedPort,
		&podNumericPort,
		&podNamedAndNumericPort,
		&podMultiPort,
		&podMultiSplitPort,
		deployment(&podNamedPort),
		deployment(&podNumericPort),
		deployment(&podNamedAndNumericPort),
		deployment(&podMultiPort),
		deployment(&podMultiSplitPort),
	)
	tests := []struct {
		name           string
		request        *core.Pod
		expectedConfig *agent.Config
		expectedError  string
	}{
		{
			"Error Precondition: No port specified",
			&core.Pod{
				ObjectMeta: podObjectMeta("named-port", "service"),
				Spec: core.PodSpec{
					Containers: []core.Container{
						{Ports: []core.ContainerPort{}},
					},
				},
			},
			nil,
			"found no service with a port that matches a container in pod <PODNAME>",
		},
		{
			"Error Precondition: Sidecar has port collision",
			&core.Pod{
				ObjectMeta: podObjectMeta("named-port", "service"),
				Spec: core.PodSpec{
					Containers: []core.Container{
						{
							Ports: []core.ContainerPort{
								{Name: "http", ContainerPort: env.AgentPort},
							}},
					},
				},
			},
			nil,
			"is exposing the same port (9900) as the traffic-agent sidecar",
		},
		{
			"Named port",
			&podNamedPort,
			&agent.Config{
				AgentName:    "named-port",
				AgentImage:   "docker.io/datawire/tel2:2.3.1",
				Namespace:    "some-ns",
				WorkloadName: "named-port",
				WorkloadKind: "Deployment",
				ManagerHost:  "traffic-manager.default",
				ManagerPort:  8081,
				Containers: []*agent.Container{
					{
						Name: "some-container",
						Intercepts: []*agent.Intercept{
							{
								ContainerPortName: "http",
								ServiceName:       "named-port",
								ServiceUID:        namedPortUID,
								ServicePortName:   "http",
								ServicePort:       80,
								Protocol:          "TCP",
								AgentPort:         9900,
								ContainerPort:     8888,
							},
						},
						EnvPrefix:  "A_",
						MountPoint: "/tel_app_mounts/some-container",
						Mounts:     []string{"/home/bob"},
					},
				},
			},
			"",
		},
		{
			"Numeric port",
			&podNumericPort,
			&agent.Config{
				AgentName:    "numeric-port",
				AgentImage:   "docker.io/datawire/tel2:2.3.1",
				Namespace:    "some-ns",
				WorkloadName: "numeric-port",
				WorkloadKind: "Deployment",
				ManagerHost:  "traffic-manager.default",
				ManagerPort:  8081,
				Containers: []*agent.Container{
					{
						Name: "some-container",
						Intercepts: []*agent.Intercept{
							{
								ContainerPortName: "",
								ServiceName:       "numeric-port",
								ServiceUID:        numericPortUID,
								ServicePortName:   "http",
								ServicePort:       80,
								Protocol:          "TCP",
								AgentPort:         9900,
								ContainerPort:     8899,
							},
						},
						EnvPrefix:  "A_",
						MountPoint: "/tel_app_mounts/some-container",
					},
				},
			},
			"",
		},
		{
			"Named and numeric port containers",
			&podNamedAndNumericPort,
			&agent.Config{
				AgentName:    "named-and-numeric",
				AgentImage:   "docker.io/datawire/tel2:2.3.1",
				Namespace:    "some-ns",
				WorkloadName: "named-and-numeric",
				WorkloadKind: "Deployment",
				ManagerHost:  "traffic-manager.default",
				ManagerPort:  8081,
				Containers: []*agent.Container{
					{
						Name: "named-port-container",
						Intercepts: []*agent.Intercept{
							{
								ContainerPortName: "http",
								ServiceName:       "named-port",
								ServiceUID:        namedPortUID,
								ServicePortName:   "http",
								ServicePort:       80,
								Protocol:          "TCP",
								AgentPort:         9900,
								ContainerPort:     8888,
							},
						},
						EnvPrefix:  "A_",
						MountPoint: "/tel_app_mounts/named-port-container",
						Mounts:     []string{"/home/bob"},
					},
					{
						Name: "numeric-port-container",
						Intercepts: []*agent.Intercept{
							{
								ContainerPortName: "",
								ServiceName:       "numeric-port",
								ServiceUID:        numericPortUID,
								ServicePortName:   "http",
								ServicePort:       80,
								Protocol:          "TCP",
								AgentPort:         9901,
								ContainerPort:     8899,
							},
						},
						EnvPrefix:  "B_",
						MountPoint: "/tel_app_mounts/numeric-port-container",
					},
				},
			},
			"",
		},
		{
			"Multi-port container and service",
			&podMultiPort,
			&agent.Config{
				AgentName:    "multi-port",
				AgentImage:   "docker.io/datawire/tel2:2.3.1",
				Namespace:    "some-ns",
				WorkloadName: "multi-port",
				WorkloadKind: "Deployment",
				ManagerHost:  "traffic-manager.default",
				ManagerPort:  8081,
				Containers: []*agent.Container{
					{
						Name: "multi-port-container",
						Intercepts: []*agent.Intercept{
							{
								ContainerPortName: "http",
								ServiceName:       "multi-port",
								ServiceUID:        multiPortUID,
								ServicePortName:   "http",
								ServicePort:       80,
								Protocol:          "TCP",
								AgentPort:         9900,
								ContainerPort:     8080,
							},
							{
								ContainerPortName: "grpc",
								ServiceName:       "multi-port",
								ServiceUID:        multiPortUID,
								ServicePortName:   "grpc",
								ServicePort:       8001,
								Protocol:          "TCP",
								AppProtocol:       "grpc",
								AgentPort:         9901,
								ContainerPort:     8081,
							},
						},
						EnvPrefix:  "A_",
						MountPoint: "/tel_app_mounts/multi-port-container",
						Mounts:     []string{"/home/bob"},
					},
				},
			},
			"",
		},
		{
			"Two containers and multi-port service",
			&podMultiSplitPort,
			&agent.Config{
				AgentName:    "multi-container",
				AgentImage:   "docker.io/datawire/tel2:2.3.1",
				Namespace:    "some-ns",
				WorkloadName: "multi-container",
				WorkloadKind: "Deployment",
				ManagerHost:  "traffic-manager.default",
				ManagerPort:  8081,
				Containers: []*agent.Container{
					{
						Name: "http-container",
						Intercepts: []*agent.Intercept{
							{
								ContainerPortName: "http",
								ServiceName:       "multi-port",
								ServiceUID:        multiPortUID,
								ServicePortName:   "http",
								ServicePort:       80,
								Protocol:          "TCP",
								AgentPort:         9900,
								ContainerPort:     8080,
							},
						},
						EnvPrefix:  "A_",
						MountPoint: "/tel_app_mounts/http-container",
						Mounts:     []string{"/home/bob"},
					},
					{
						Name: "grpc-container",
						Intercepts: []*agent.Intercept{
							{
								ContainerPortName: "grpc",
								ServiceName:       "multi-port",
								ServiceUID:        multiPortUID,
								ServicePortName:   "grpc",
								ServicePort:       8001,
								Protocol:          "TCP",
								AppProtocol:       "grpc",
								AgentPort:         9901,
								ContainerPort:     8081,
							},
						},
						EnvPrefix:  "B_",
						MountPoint: "/tel_app_mounts/grpc-container",
					},
				},
			},
			"",
		},
	}
	for _, test := range tests {
		test := test // pin it
		ctx := k8sapi.WithK8sInterface(ctx, clientset)
		t.Run(test.name, func(t *testing.T) {
			actualConfig, actualErr := agentconfig.GenerateForPod(ctx, test.request)
			requireContains(t, actualErr, strings.ReplaceAll(test.expectedError, "<PODNAME>", test.request.Name))
			if actualConfig == nil {
				actualConfig = &agent.Config{}
			}
			expectedConfig := test.expectedConfig
			if expectedConfig == nil {
				expectedConfig = &agent.Config{}
			}
			assert.Equal(t, expectedConfig, actualConfig, "configs differ")
		})
	}
}

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
	one := int32(1)

	podSuffix := "-6699c6cb54-"
	podName := func(name string) string {
		return name + podSuffix
	}

	wlName := func(podName string) string {
		return strings.TrimSuffix(podName, podSuffix)
	}

	podOwner := func(name string) []meta.OwnerReference {
		return []meta.OwnerReference{
			{
				APIVersion: "apps/v1",
				Kind:       "Deployment",
				Name:       name,
				Controller: boolP(true),
			},
		}
	}

	podObjectMeta := func(name string) meta.ObjectMeta {
		return meta.ObjectMeta{
			Name:            podName(name),
			Namespace:       "some-ns",
			Annotations:     map[string]string{install.InjectAnnotation: "enabled"},
			Labels:          map[string]string{"service": name},
			OwnerReferences: podOwner(name),
		}
	}

	podNamedPort := core.Pod{
		ObjectMeta: podObjectMeta("named-port"),
		Spec: core.PodSpec{
			Containers: []core.Container{
				{
					Name: "some-container",
					Env: []core.EnvVar{
						{
							Name:  "SOME_NAME",
							Value: "some value",
						},
					},
					Ports: []core.ContainerPort{
						{
							Name: "http", ContainerPort: 8888,
						},
					},
				},
			},
		},
	}

	podNumericPort := core.Pod{
		ObjectMeta: podObjectMeta("numeric-port"),
		Spec: core.PodSpec{
			Containers: []core.Container{
				{
					Name: "some-container",
					Ports: []core.ContainerPort{
						{
							ContainerPort: 8888,
						},
					},
				},
			},
		},
	}

	deployment := func(pod *core.Pod) *apps.Deployment {
		name := wlName(pod.Name)
		return &apps.Deployment{
			TypeMeta: meta.TypeMeta{
				Kind:       "Deployment",
				APIVersion: "apps/v1",
			},
			ObjectMeta: meta.ObjectMeta{
				Name:        name,
				Namespace:   "some-ns",
				Labels:      nil,
				Annotations: nil,
			},
			Spec: apps.DeploymentSpec{
				Replicas: &one,
				Template: core.PodTemplateSpec{
					ObjectMeta: pod.ObjectMeta,
					Spec:       pod.Spec,
				},
				Selector: &meta.LabelSelector{MatchLabels: map[string]string{
					"service": name,
				}},
			},
		}
	}

	clientset := fake.NewSimpleClientset(
		&core.Service{
			TypeMeta: meta.TypeMeta{
				Kind:       "Service",
				APIVersion: "v1",
			},
			ObjectMeta: meta.ObjectMeta{
				Name:        "named-port",
				Namespace:   "some-ns",
				Labels:      nil,
				Annotations: nil,
			},
			Spec: core.ServiceSpec{
				Ports: []core.ServicePort{{
					Protocol:   "TCP",
					Name:       "proxied",
					Port:       80,
					TargetPort: intstr.FromString("http"),
				}},
				Selector: map[string]string{
					"service": "named-port",
				},
			},
		},
		&core.Service{
			TypeMeta: meta.TypeMeta{
				Kind:       "Service",
				APIVersion: "v1",
			},
			ObjectMeta: meta.ObjectMeta{
				Name:        "numeric-port",
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
					"service": "numeric-port",
				},
			},
		},
		&podNamedPort,
		&podNumericPort,
		deployment(&podNamedPort),
		deployment(&podNumericPort),
	)

	tests := []struct {
		name           string
		pod            *core.Pod
		generateConfig bool
		expectedPatch  string
		expectedError  string
		envAdditions   *managerutil.Env
	}{
		{
			"Skip Precondition: No annotation",
			&core.Pod{
				ObjectMeta: meta.ObjectMeta{
					Namespace:       "some-ns",
					Name:            "named-port",
					Labels:          map[string]string{"service": "named-port"},
					OwnerReferences: podOwner("named-port"),
				},
			},
			false,
			"",
			"",
			nil,
		},
		{
			"Skip Precondition: No name/namespace",
			&core.Pod{
				ObjectMeta: meta.ObjectMeta{Annotations: map[string]string{
					install.InjectAnnotation: "enabled",
				}},
			},
			false,
			"",
			`unable to extract pod name and/or namespace (got ".default")`,
			nil,
		},
		{
			"Apply Patch: Named port",
			&core.Pod{
				ObjectMeta: podObjectMeta("named-port"),
				Spec: core.PodSpec{
					Containers: []core.Container{{
						Name:  "some-app-name",
						Image: "some-app-image",
						Env: []core.EnvVar{
							{
								Name:  "SOME_NAME",
								Value: "some value",
							},
						},
						Ports: []core.ContainerPort{{
							Name: "http", ContainerPort: 8888},
						}},
					},
				},
			},
			true,
			`- op: add
  path: /spec/containers/-
  value:
    args:
    - agent
    env:
    - name: _TEL_APP_A_SOME_NAME
      value: some value
    - name: _TEL_AGENT_POD_IP
      valueFrom:
        fieldRef:
          apiVersion: v1
          fieldPath: status.podIP
    image: docker.io/datawire/tel2:2.3.1
    name: traffic-agent
    ports:
    - containerPort: 9900
      name: http
      protocol: TCP
    readinessProbe:
      exec:
        command:
        - /bin/stat
        - /tmp/agent/ready
    resources: {}
    volumeMounts:
    - mountPath: /tel_pod_info
      name: traffic-annotations
    - mountPath: /etc/traffic-agent
      name: traffic-config
- op: add
  path: /spec/volumes/-
  value:
    downwardAPI:
      items:
      - fieldRef:
          apiVersion: v1
          fieldPath: metadata.annotations
        path: annotations
    name: traffic-annotations
- op: add
  path: /spec/volumes/-
  value:
    configMap:
      items:
      - key: named-port
        path: config.yaml
      name: telepresence-agents
    name: traffic-config
- op: replace
  path: /spec/containers/0/ports/0/name
  value: tm-http
`,
			"",
			nil,
		},
		{
			"Apply Patch: Telepresence API Port",
			&core.Pod{
				ObjectMeta: podObjectMeta("named-port"),
				Spec: core.PodSpec{
					Containers: []core.Container{{
						Name:  "some-app-name",
						Image: "some-app-image",
						Ports: []core.ContainerPort{{
							Name: "http", ContainerPort: 8888},
						}},
					},
				},
			},
			true,
			`- op: add
  path: /spec/containers/-
  value:
    args:
    - agent
    env:
    - name: _TEL_AGENT_POD_IP
      valueFrom:
        fieldRef:
          apiVersion: v1
          fieldPath: status.podIP
    image: docker.io/datawire/tel2:2.3.1
    name: traffic-agent
    ports:
    - containerPort: 9900
      name: http
      protocol: TCP
    readinessProbe:
      exec:
        command:
        - /bin/stat
        - /tmp/agent/ready
    resources: {}
    volumeMounts:
    - mountPath: /tel_pod_info
      name: traffic-annotations
    - mountPath: /etc/traffic-agent
      name: traffic-config
- op: add
  path: /spec/volumes/-
  value:
    downwardAPI:
      items:
      - fieldRef:
          apiVersion: v1
          fieldPath: metadata.annotations
        path: annotations
    name: traffic-annotations
- op: add
  path: /spec/volumes/-
  value:
    configMap:
      items:
      - key: named-port
        path: config.yaml
      name: telepresence-agents
    name: traffic-config
- op: replace
  path: /spec/containers/0/ports/0/name
  value: tm-http
- op: replace
  path: /spec/containers/0/env
  value: []
- op: add
  path: /spec/containers/0/env/-
  value:
    name: TELEPRESENCE_API_PORT
    value: "9981"
`,
			"",
			&managerutil.Env{
				APIPort: 9981,
			},
		},
		{
			"Error Precondition: Invalid service name",
			&core.Pod{
				ObjectMeta: meta.ObjectMeta{
					Name:      "named-port",
					Namespace: "some-ns",
					Labels:    map[string]string{"service": "named-port"},
					Annotations: map[string]string{
						install.InjectAnnotation:      "enabled",
						install.ServiceNameAnnotation: "khruangbin",
					},
					OwnerReferences: podOwner("named-port"),
				},
				Spec: core.PodSpec{
					Containers: []core.Container{{
						Name:  "some-app-name",
						Image: "some-app-image",
						Ports: []core.ContainerPort{{
							Name: "http", ContainerPort: 8888},
						}},
					},
				},
			},
			true,
			"",
			"unable to find service khruangbin specified by annotation telepresence.getambassador.io/inject-service-name declared in pod <PODNAME>",
			nil,
		},
		{
			"Apply Patch: Multiple services",
			&core.Pod{
				ObjectMeta: meta.ObjectMeta{
					Name:      "named-port",
					Namespace: "some-ns",
					Labels:    map[string]string{"service": "named-port"},
					Annotations: map[string]string{
						install.InjectAnnotation:      "enabled",
						install.ServiceNameAnnotation: "named-port",
					},
					OwnerReferences: podOwner("named-port"),
				},
				Spec: core.PodSpec{
					Containers: []core.Container{{
						Name:  "some-app-name",
						Image: "some-app-image",
						Ports: []core.ContainerPort{{
							Name: "http", ContainerPort: 8888},
						}},
					},
				},
			},
			true,
			`- op: add
  path: /spec/containers/-
  value:
    args:
    - agent
    env:
    - name: _TEL_AGENT_POD_IP
      valueFrom:
        fieldRef:
          apiVersion: v1
          fieldPath: status.podIP
    image: docker.io/datawire/tel2:2.3.1
    name: traffic-agent
    ports:
    - containerPort: 9900
      name: http
      protocol: TCP
    readinessProbe:
      exec:
        command:
        - /bin/stat
        - /tmp/agent/ready
    resources: {}
    volumeMounts:
    - mountPath: /tel_pod_info
      name: traffic-annotations
    - mountPath: /etc/traffic-agent
      name: traffic-config
- op: add
  path: /spec/volumes/-
  value:
    downwardAPI:
      items:
      - fieldRef:
          apiVersion: v1
          fieldPath: metadata.annotations
        path: annotations
    name: traffic-annotations
- op: add
  path: /spec/volumes/-
  value:
    configMap:
      items:
      - key: named-port
        path: config.yaml
      name: telepresence-agents
    name: traffic-config
- op: replace
  path: /spec/containers/0/ports/0/name
  value: tm-http
`,
			"",
			nil,
		},
		{
			"Apply Patch: Numeric port",
			&core.Pod{
				ObjectMeta: podObjectMeta("numeric-port"),
				Spec: core.PodSpec{
					Containers: []core.Container{{
						Name:  "some-app-name",
						Image: "some-app-image",
						Ports: []core.ContainerPort{{ContainerPort: 8888}}},
					},
				},
			},
			true,
			`- op: replace
  path: /spec/initContainers
  value:
  - args:
    - agent-init
    image: docker.io/datawire/tel2:2.3.1
    name: tel-agent-init
    resources: {}
    securityContext:
      capabilities:
        add:
        - NET_ADMIN
    volumeMounts:
    - mountPath: /etc/traffic-agent
      name: traffic-config
- op: add
  path: /spec/containers/-
  value:
    args:
    - agent
    env:
    - name: _TEL_AGENT_POD_IP
      valueFrom:
        fieldRef:
          apiVersion: v1
          fieldPath: status.podIP
    image: docker.io/datawire/tel2:2.3.1
    name: traffic-agent
    ports:
    - containerPort: 9900
      protocol: TCP
    readinessProbe:
      exec:
        command:
        - /bin/stat
        - /tmp/agent/ready
    resources: {}
    volumeMounts:
    - mountPath: /tel_pod_info
      name: traffic-annotations
    - mountPath: /etc/traffic-agent
      name: traffic-config
- op: add
  path: /spec/volumes/-
  value:
    downwardAPI:
      items:
      - fieldRef:
          apiVersion: v1
          fieldPath: metadata.annotations
        path: annotations
    name: traffic-annotations
- op: add
  path: /spec/volumes/-
  value:
    configMap:
      items:
      - key: numeric-port
        path: config.yaml
      name: telepresence-agents
    name: traffic-config
`,
			"",
			nil,
		},
		{
			"Apply Patch: Numeric port with init containers",
			&core.Pod{
				ObjectMeta: podObjectMeta("numeric-port"),
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
			},
			true,
			`- op: add
  path: /spec/initContainers/-
  value:
    args:
    - agent-init
    image: docker.io/datawire/tel2:2.3.1
    name: tel-agent-init
    resources: {}
    securityContext:
      capabilities:
        add:
        - NET_ADMIN
    volumeMounts:
    - mountPath: /etc/traffic-agent
      name: traffic-config
- op: add
  path: /spec/containers/-
  value:
    args:
    - agent
    env:
    - name: _TEL_AGENT_POD_IP
      valueFrom:
        fieldRef:
          apiVersion: v1
          fieldPath: status.podIP
    image: docker.io/datawire/tel2:2.3.1
    name: traffic-agent
    ports:
    - containerPort: 9900
      protocol: TCP
    readinessProbe:
      exec:
        command:
        - /bin/stat
        - /tmp/agent/ready
    resources: {}
    volumeMounts:
    - mountPath: /tel_pod_info
      name: traffic-annotations
    - mountPath: /etc/traffic-agent
      name: traffic-config
- op: add
  path: /spec/volumes/-
  value:
    downwardAPI:
      items:
      - fieldRef:
          apiVersion: v1
          fieldPath: metadata.annotations
        path: annotations
    name: traffic-annotations
- op: add
  path: /spec/volumes/-
  value:
    configMap:
      items:
      - key: numeric-port
        path: config.yaml
      name: telepresence-agents
    name: traffic-config
`,
			"",
			nil,
		},
		{
			"Apply Patch: re-processing, null patch",
			&core.Pod{
				ObjectMeta: podObjectMeta("numeric-port"),
				Spec: core.PodSpec{
					InitContainers: []core.Container{{
						Name:  agent.InitContainerName,
						Image: env.AgentRegistry + "/" + env.AgentImage,
						Args:  []string{"agent-init"},
						VolumeMounts: []core.VolumeMount{{
							Name:      agent.ConfigVolumeName,
							MountPath: agent.ConfigMountPoint,
						}},
						SecurityContext: &core.SecurityContext{
							Capabilities: &core.Capabilities{
								Add: []core.Capability{"NET_ADMIN"},
							},
						},
					}},
					Containers: []core.Container{
						{
							Name:  "some-app-name",
							Image: "some-app-image",
							Ports: []core.ContainerPort{{ContainerPort: 8888}},
						},
						{
							Name:            install.AgentContainerName,
							Image:           "docker.io/datawire/tel2:2.3.1",
							ImagePullPolicy: "IfNotPresent",
							Args:            []string{"agent"},
							Ports: []core.ContainerPort{{
								ContainerPort: 9900,
								Protocol:      "TCP",
							}},
							EnvFrom: nil,
							Env: []core.EnvVar{{
								Name: "_TEL_AGENT_POD_IP",
								ValueFrom: &core.EnvVarSource{
									FieldRef: &core.ObjectFieldSelector{
										APIVersion: "v1",
										FieldPath:  "status.podIP",
									},
								},
							}},
							Resources:                core.ResourceRequirements{},
							TerminationMessagePath:   "/dev/termination-log",
							TerminationMessagePolicy: "File",
							VolumeMounts: []core.VolumeMount{
								{
									Name:      "traffic-annotations",
									MountPath: "/tel_pod_info",
								},
								{
									Name:      "traffic-config",
									MountPath: "/etc/traffic-agent",
								},
							},
							ReadinessProbe: &core.Probe{
								ProbeHandler: core.ProbeHandler{
									Exec: &core.ExecAction{Command: []string{"/bin/stat", "/tmp/agent/ready"}},
								},
							},
						},
					},
					Volumes: []core.Volume{{
						Name: install.AgentAnnotationVolumeName,
					}},
				},
			},
			true,
			"null\n",
			"",
			nil,
		},
		{
			"Apply Patch: volumes are copied",
			&core.Pod{
				ObjectMeta: podObjectMeta("named-port"),
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
			},
			true,
			`- op: add
  path: /spec/containers/-
  value:
    args:
    - agent
    env:
    - name: _TEL_AGENT_POD_IP
      valueFrom:
        fieldRef:
          apiVersion: v1
          fieldPath: status.podIP
    image: docker.io/datawire/tel2:2.3.1
    name: traffic-agent
    ports:
    - containerPort: 9900
      name: http
      protocol: TCP
    readinessProbe:
      exec:
        command:
        - /bin/stat
        - /tmp/agent/ready
    resources: {}
    volumeMounts:
    - mountPath: /tel_pod_info
      name: traffic-annotations
    - mountPath: /etc/traffic-agent
      name: traffic-config
- op: add
  path: /spec/volumes/-
  value:
    downwardAPI:
      items:
      - fieldRef:
          apiVersion: v1
          fieldPath: metadata.annotations
        path: annotations
    name: traffic-annotations
- op: add
  path: /spec/volumes/-
  value:
    configMap:
      items:
      - key: named-port
        path: config.yaml
      name: telepresence-agents
    name: traffic-config
- op: replace
  path: /spec/containers/0/ports/0/name
  value: tm-http
`,
			"",
			nil,
		},
	}

	for _, test := range tests {
		test := test // pin it
		t.Run(test.name, func(t *testing.T) {
			ctx := dlog.NewTestContext(t, false)
			ctx = managerutil.WithEnv(ctx, env)
			ctx = k8sapi.WithK8sInterface(ctx, clientset)
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
			var actualPatch patchOps
			var actualErr error
			cw := agentconfig.NewWatcher("")
			if test.generateConfig {
				var ac *agent.Config
				if ac, actualErr = agentconfig.GenerateForPod(ctx, test.pod); actualErr == nil {
					actualErr = cw.Store(ctx, ac, true)
				}
			}
			if actualErr == nil {
				request := toAdmissionRequest(podResource, test.pod)
				a := agentInjector{agentConfigs: cw}
				actualPatch, actualErr = a.inject(ctx, request)
			}
			requireContains(t, actualErr, strings.ReplaceAll(test.expectedError, "<PODNAME>", test.pod.Name))
			if actualPatch != nil || test.expectedPatch != "" {
				patchBytes, err := yaml.Marshal(actualPatch)
				require.NoError(t, err)
				patchString := string(patchBytes)
				if test.expectedPatch != patchString {
					fmt.Println(patchString)
				}
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
	require.Errorf(t, err, "expected error %q", expected)
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
