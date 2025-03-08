package mutator

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/go-json-experiment/json"
	jsonv1 "github.com/go-json-experiment/json/v1"
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
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	"sigs.k8s.io/yaml"

	argorolloutsfake "github.com/datawire/argo-rollouts-go-client/pkg/client/clientset/versioned/fake"
	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/config"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/managerutil"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/namespaces"
	"github.com/telepresenceio/telepresence/v2/pkg/agentconfig"
	"github.com/telepresenceio/telepresence/v2/pkg/agentmap"
	"github.com/telepresenceio/telepresence/v2/pkg/informer"
	"github.com/telepresenceio/telepresence/v2/pkg/k8sapi"
	"github.com/telepresenceio/telepresence/v2/pkg/labels"
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

const mgrNs = "default"

func TestTrafficAgentConfigGenerator(t *testing.T) {
	managerConfig := core.ConfigMap{
		ObjectMeta: meta.ObjectMeta{
			Name:      agentconfig.ManagerAppName,
			Namespace: mgrNs,
		},
		Data: map[string]string{"namespace-selector.yaml": ` 
matchExpressions:
- key: kubernetes.io/metadata.name
  operator: In
  values:
    - some-ns`},
	}

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
			Annotations:     map[string]string{agentconfig.InjectAnnotation: "enabled"},
			Labels:          map[string]string{labelKey: name},
			OwnerReferences: podOwner(name),
		}
	}

	secretMode := int32(0o644)
	yes := true
	no := false
	podNoPort := core.Pod{
		ObjectMeta: podObjectMeta("no-port", "service"),
		Spec: core.PodSpec{
			AutomountServiceAccountToken: &yes,
			Containers: []core.Container{
				{
					Name: "some-container",
					VolumeMounts: []core.VolumeMount{
						{
							Name:      "default-token-nkspp",
							MountPath: serviceAccountMountPath,
						},
					},
				},
			},
			Volumes: []core.Volume{
				{
					Name: "default-token-nkspp",
					VolumeSource: core.VolumeSource{
						Secret: &core.SecretVolumeSource{
							SecretName:  "default-token-nkspp",
							DefaultMode: &secretMode,
						},
					},
				},
			},
		},
	}

	podNamedPort := core.Pod{
		ObjectMeta: podObjectMeta("named-port", "service"),
		Spec: core.PodSpec{
			AutomountServiceAccountToken: &yes,
			Containers: []core.Container{
				{
					Name: "some-container",
					Ports: []core.ContainerPort{
						{
							Name: "http", ContainerPort: 8888,
						},
					},
					VolumeMounts: []core.VolumeMount{
						{
							Name:      "default-token-nkspp",
							MountPath: serviceAccountMountPath,
						},
					},
				},
			},
			Volumes: []core.Volume{
				{
					Name: "default-token-nkspp",
					VolumeSource: core.VolumeSource{
						Secret: &core.SecretVolumeSource{
							SecretName:  "default-token-nkspp",
							DefaultMode: &secretMode,
						},
					},
				},
			},
		},
	}

	podNumericPort := core.Pod{
		ObjectMeta: podObjectMeta("numeric-port", "app"),
		Spec: core.PodSpec{
			AutomountServiceAccountToken: &no,
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

	podGRPCPort := core.Pod{
		ObjectMeta: podObjectMeta("grpc-port", "app"),
		Spec: core.PodSpec{
			AutomountServiceAccountToken: &no,
			Containers: []core.Container{
				{
					Name: "some-container",
					Ports: []core.ContainerPort{
						{
							ContainerPort: 8443,
						},
					},
				},
			},
		},
	}

	podUnnamedNumericPort := core.Pod{
		ObjectMeta: podObjectMeta("unnamed-numeric-port", "app"),
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
			Annotations:     map[string]string{agentconfig.InjectAnnotation: "enabled"},
			Labels:          map[string]string{"service": "named-port", "app": "numeric-port"},
			OwnerReferences: podOwner("named-and-numeric"),
		},
		Spec: core.PodSpec{
			AutomountServiceAccountToken: &yes,
			Containers: []core.Container{
				{
					Name: "named-port-container",
					Ports: []core.ContainerPort{
						{
							Name:          "http",
							ContainerPort: 8888,
						},
					},
					VolumeMounts: []core.VolumeMount{
						{
							Name:      "bob",
							MountPath: "/home/bob",
						},
						{
							Name:      "default-token-nkspp",
							MountPath: serviceAccountMountPath,
						},
					},
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
			Volumes: []core.Volume{
				{
					Name: "default-token-nkspp",
					VolumeSource: core.VolumeSource{
						Secret: &core.SecretVolumeSource{
							SecretName:  "default-token-nkspp",
							DefaultMode: &secretMode,
						},
					},
				},
				{
					Name: "bob",
					VolumeSource: core.VolumeSource{
						EmptyDir: &core.EmptyDirVolumeSource{},
					},
				},
			},
		},
	}

	podMultiPort := core.Pod{
		ObjectMeta: meta.ObjectMeta{
			Name:            podName("multi-port"),
			Namespace:       "some-ns",
			Annotations:     map[string]string{agentconfig.InjectAnnotation: "enabled"},
			Labels:          map[string]string{"service": "multi-port"},
			OwnerReferences: podOwner("multi-port"),
		},
		Spec: core.PodSpec{
			AutomountServiceAccountToken: &no,
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
			Volumes: []core.Volume{
				{
					Name: "bob",
					VolumeSource: core.VolumeSource{
						EmptyDir: &core.EmptyDirVolumeSource{},
					},
				},
			},
		},
	}

	podMultiSplitPort := core.Pod{
		ObjectMeta: meta.ObjectMeta{
			Name:            podName("multi-container"),
			Namespace:       "some-ns",
			Annotations:     map[string]string{agentconfig.InjectAnnotation: "enabled"},
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
	grpcPortUID := makeUID()
	unnamedNumericPortUID := makeUID()
	multiPortUID := makeUID()

	clientset := fake.NewClientset(
		&core.Namespace{
			TypeMeta: meta.TypeMeta{
				Kind:       "Namespace",
				APIVersion: "v1",
			},
			ObjectMeta: meta.ObjectMeta{
				Name: "some-ns",
				Labels: map[string]string{
					labels.NameLabelKey: "some-ns",
				},
			},
		},
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
					TargetPort: intstr.FromInt32(8899),
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
				Name:      "unnamed-numeric-port",
				Namespace: "some-ns",
				UID:       unnamedNumericPortUID,
			},
			Spec: core.ServiceSpec{
				Ports: []core.ServicePort{{
					Protocol:   "TCP",
					Port:       80,
					TargetPort: intstr.FromInt32(8899),
				}},
				Selector: map[string]string{
					"app": "unnamed-numeric-port",
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
					},
				},
				Selector: map[string]string{
					"service": "multi-port",
				},
			},
		},
		&core.Service{
			TypeMeta: meta.TypeMeta{
				Kind:       "Service",
				APIVersion: "v1",
			},
			ObjectMeta: meta.ObjectMeta{
				Name:      "grpc-port",
				Namespace: "some-ns",
				UID:       grpcPortUID,
			},
			Spec: core.ServiceSpec{
				Ports: []core.ServicePort{
					{
						Protocol:   "TCP",
						Name:       "grpc",
						Port:       443,
						TargetPort: intstr.FromInt32(8443),
					},
				},
				Selector: map[string]string{
					"app": "grpc-port",
				},
			},
		},
		&managerConfig,
		&podNoPort,
		&podNamedPort,
		&podNumericPort,
		&podGRPCPort,
		&podNamedAndNumericPort,
		&podMultiPort,
		&podMultiSplitPort,
		deployment(&podNoPort),
		deployment(&podNamedPort),
		deployment(&podNumericPort),
		deployment(&podGRPCPort),
		deployment(&podUnnamedNumericPort),
		deployment(&podNamedAndNumericPort),
		deployment(&podMultiPort),
		deployment(&podMultiSplitPort),
	)
	type testInput struct {
		name           string
		request        *core.Pod
		expectedConfig *agentconfig.Sidecar
		expectedError  string
	}

	tests := []testInput{
		{
			"Error Precondition: No port specified",
			&podNoPort,
			&agentconfig.Sidecar{
				AgentName:    "no-port",
				AgentImage:   "ghcr.io/telepresenceio/tel2:2.13.3",
				Namespace:    "some-ns",
				WorkloadName: "no-port",
				WorkloadKind: "Deployment",
				ManagerHost:  "traffic-manager.default",
				ManagerPort:  8081,
				Containers: []*agentconfig.Container{
					{
						Name:       "some-container",
						EnvPrefix:  "A_",
						MountPoint: "/tel_app_mounts/some-container",
						Mounts: map[string]agentconfig.MountPolicy{
							"/var/run/secrets/kubernetes.io/serviceaccount": agentconfig.MountPolicyRemote,
						},
						MountPaths: []string{"/var/run/secrets/kubernetes.io/serviceaccount"},
					},
				},
			},
			"",
		},
		{
			"Error Precondition: Sidecar has port collision",
			&core.Pod{
				ObjectMeta: podObjectMeta("named-port", "service"),
				Spec: core.PodSpec{
					Containers: []core.Container{
						{
							Ports: []core.ContainerPort{
								{Name: "http", ContainerPort: 9900},
							},
						},
					},
				},
			},
			nil,
			"is exposing the same port (9900) as the traffic-agent sidecar",
		},
		{
			"Named port",
			&podNamedPort,
			&agentconfig.Sidecar{
				AgentName:    "named-port",
				AgentImage:   "ghcr.io/telepresenceio/tel2:2.13.3",
				Namespace:    "some-ns",
				WorkloadName: "named-port",
				WorkloadKind: "Deployment",
				ManagerHost:  "traffic-manager.default",
				ManagerPort:  8081,
				Containers: []*agentconfig.Container{
					{
						Name: "some-container",
						Intercepts: []*agentconfig.Intercept{
							{
								ContainerPortName: "http",
								ServiceName:       "named-port",
								ServiceUID:        namedPortUID,
								ServicePortName:   "http",
								ServicePort:       80,
								Protocol:          core.ProtocolTCP,
								AgentPort:         9900,
								ContainerPort:     8888,
							},
						},
						EnvPrefix:  "A_",
						MountPoint: "/tel_app_mounts/some-container",
						Mounts: map[string]agentconfig.MountPolicy{
							"/var/run/secrets/kubernetes.io/serviceaccount": agentconfig.MountPolicyRemote,
						},
						MountPaths: []string{"/var/run/secrets/kubernetes.io/serviceaccount"},
					},
				},
			},
			"",
		},
		{
			"Numeric port",
			&podNumericPort,
			&agentconfig.Sidecar{
				AgentName:    "numeric-port",
				AgentImage:   "ghcr.io/telepresenceio/tel2:2.13.3",
				Namespace:    "some-ns",
				WorkloadName: "numeric-port",
				WorkloadKind: "Deployment",
				ManagerHost:  "traffic-manager.default",
				ManagerPort:  8081,
				Containers: []*agentconfig.Container{
					{
						Name: "some-container",
						Intercepts: []*agentconfig.Intercept{
							{
								ContainerPortName: "",
								ServiceName:       "numeric-port",
								ServiceUID:        numericPortUID,
								ServicePortName:   "http",
								ServicePort:       80,
								TargetPortNumeric: true,
								Protocol:          core.ProtocolTCP,
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
			"Unnamed Numeric port",
			&podUnnamedNumericPort,
			&agentconfig.Sidecar{
				AgentName:    "unnamed-numeric-port",
				AgentImage:   "ghcr.io/telepresenceio/tel2:2.13.3",
				Namespace:    "some-ns",
				WorkloadName: "unnamed-numeric-port",
				WorkloadKind: "Deployment",
				ManagerHost:  "traffic-manager.default",
				ManagerPort:  8081,
				Containers: []*agentconfig.Container{
					{
						Name: "some-container",
						Intercepts: []*agentconfig.Intercept{
							{
								ContainerPortName: "",
								ServiceName:       "unnamed-numeric-port",
								ServiceUID:        unnamedNumericPortUID,
								ServicePort:       80,
								TargetPortNumeric: true,
								Protocol:          core.ProtocolTCP,
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
			&agentconfig.Sidecar{
				AgentName:    "named-and-numeric",
				AgentImage:   "ghcr.io/telepresenceio/tel2:2.13.3",
				Namespace:    "some-ns",
				WorkloadName: "named-and-numeric",
				WorkloadKind: "Deployment",
				ManagerHost:  "traffic-manager.default",
				ManagerPort:  8081,
				Containers: []*agentconfig.Container{
					{
						Name: "named-port-container",
						Intercepts: []*agentconfig.Intercept{
							{
								ContainerPortName: "http",
								ServiceName:       "named-port",
								ServiceUID:        namedPortUID,
								ServicePortName:   "http",
								ServicePort:       80,
								Protocol:          core.ProtocolTCP,
								AgentPort:         9900,
								ContainerPort:     8888,
							},
						},
						EnvPrefix:  "A_",
						MountPoint: "/tel_app_mounts/named-port-container",
						Mounts: map[string]agentconfig.MountPolicy{
							"/home/bob": agentconfig.MountPolicyRemote,
							"/var/run/secrets/kubernetes.io/serviceaccount": agentconfig.MountPolicyRemote,
						},
						MountPaths: []string{"/home/bob", "/var/run/secrets/kubernetes.io/serviceaccount"},
					},
					{
						Name: "numeric-port-container",
						Intercepts: []*agentconfig.Intercept{
							{
								ContainerPortName: "",
								ServiceName:       "numeric-port",
								ServiceUID:        numericPortUID,
								ServicePortName:   "http",
								ServicePort:       80,
								TargetPortNumeric: true,
								Protocol:          core.ProtocolTCP,
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
			&agentconfig.Sidecar{
				AgentName:    "multi-port",
				AgentImage:   "ghcr.io/telepresenceio/tel2:2.13.3",
				Namespace:    "some-ns",
				WorkloadName: "multi-port",
				WorkloadKind: "Deployment",
				ManagerHost:  "traffic-manager.default",
				ManagerPort:  8081,
				Containers: []*agentconfig.Container{
					{
						Name: "multi-port-container",
						Intercepts: []*agentconfig.Intercept{
							{
								ContainerPortName: "http",
								ServiceName:       "multi-port",
								ServiceUID:        multiPortUID,
								ServicePortName:   "http",
								ServicePort:       80,
								Protocol:          core.ProtocolTCP,
								AgentPort:         9900,
								ContainerPort:     8080,
							},
							{
								ContainerPortName: "grpc",
								ServiceName:       "multi-port",
								ServiceUID:        multiPortUID,
								ServicePortName:   "grpc",
								ServicePort:       8001,
								Protocol:          core.ProtocolTCP,
								AppProtocol:       "grpc",
								AgentPort:         9901,
								ContainerPort:     8081,
							},
						},
						EnvPrefix:  "A_",
						MountPoint: "/tel_app_mounts/multi-port-container",
						Mounts: map[string]agentconfig.MountPolicy{
							"/home/bob": agentconfig.MountPolicyRemote,
						},
						MountPaths: []string{"/home/bob"},
					},
				},
			},
			"",
		},
		{
			"Two containers and multi-port service",
			&podMultiSplitPort,
			&agentconfig.Sidecar{
				AgentName:    "multi-container",
				AgentImage:   "ghcr.io/telepresenceio/tel2:2.13.3",
				Namespace:    "some-ns",
				WorkloadName: "multi-container",
				WorkloadKind: "Deployment",
				ManagerHost:  "traffic-manager.default",
				ManagerPort:  8081,
				Containers: []*agentconfig.Container{
					{
						Name: "http-container",
						Intercepts: []*agentconfig.Intercept{
							{
								ContainerPortName: "http",
								ServiceName:       "multi-port",
								ServiceUID:        multiPortUID,
								ServicePortName:   "http",
								ServicePort:       80,
								Protocol:          core.ProtocolTCP,
								AgentPort:         9900,
								ContainerPort:     8080,
							},
						},
						EnvPrefix:  "A_",
						MountPoint: "/tel_app_mounts/http-container",
						Mounts: map[string]agentconfig.MountPolicy{
							"/home/bob": agentconfig.MountPolicyRemote,
						},
						MountPaths: []string{"/home/bob"},
					},
					{
						Name: "grpc-container",
						Intercepts: []*agentconfig.Intercept{
							{
								ContainerPortName: "grpc",
								ServiceName:       "multi-port",
								ServiceUID:        multiPortUID,
								ServicePortName:   "grpc",
								ServicePort:       8001,
								Protocol:          core.ProtocolTCP,
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

	runFunc := func(t *testing.T, test *testInput, appProtoStrategy k8sapi.AppProtocolStrategy) {
		ctx := dlog.NewTestContext(t, false)
		env := &managerutil.Env{
			ServerHost: "tel-example",
			ServerPort: 8081,

			ManagerNamespace:         "default",
			AgentRegistry:            "ghcr.io/telepresenceio",
			AgentImageName:           "tel2",
			AgentImageTag:            "2.14.0",
			AgentPort:                9900,
			AgentAppProtocolStrategy: appProtoStrategy,

			EnabledWorkloadKinds: k8sapi.Kinds{k8sapi.DeploymentKind, k8sapi.StatefulSetKind, k8sapi.ReplicaSetKind},
		}
		ctx = managerutil.WithEnv(ctx, env)
		ctx = setupAgentInjector(t, ctx, clientset)

		gc, err := agentmap.GeneratorConfigFunc("ghcr.io/telepresenceio/tel2:2.13.3")
		require.NoError(t, err)
		actualConfig, actualErr := generateForPod(t, ctx, test.request, gc)
		requireContains(t, actualErr, strings.ReplaceAll(test.expectedError, "<PODNAME>", test.request.Name))
		if actualConfig == nil {
			actualConfig = &agentconfig.Sidecar{}
		}
		expectedConfig := test.expectedConfig
		if expectedConfig == nil {
			expectedConfig = &agentconfig.Sidecar{}
		}
		assert.Equal(t, expectedConfig, actualConfig, "configs differ")
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runFunc(t, &test, k8sapi.Http2Probe)
		})
	}

	test := testInput{
		"AppProtocolStrategy named and named grpc port without appProtocol",
		&podGRPCPort,
		&agentconfig.Sidecar{
			AgentName:    "grpc-port",
			AgentImage:   "ghcr.io/telepresenceio/tel2:2.13.3",
			Namespace:    "some-ns",
			WorkloadName: "grpc-port",
			WorkloadKind: "Deployment",
			ManagerHost:  "traffic-manager.default",
			ManagerPort:  8081,
			Containers: []*agentconfig.Container{
				{
					Name: "some-container",
					Intercepts: []*agentconfig.Intercept{
						{
							ServiceName:       "grpc-port",
							ServiceUID:        grpcPortUID,
							ServicePortName:   "grpc",
							ServicePort:       443,
							Protocol:          core.ProtocolTCP,
							AgentPort:         9900,
							ContainerPort:     8443,
							AppProtocol:       "grpc",
							TargetPortNumeric: true,
						},
					},
					EnvPrefix:  "A_",
					MountPoint: "/tel_app_mounts/some-container",
				},
			},
		},
		"",
	}
	t.Run(test.name, func(t *testing.T) {
		runFunc(t, &test, k8sapi.PortName)
	})
}

func TestTrafficAgentInjector(t *testing.T) {
	one := int32(1)
	managerConfig := core.ConfigMap{
		ObjectMeta: meta.ObjectMeta{
			Name:      agentconfig.ManagerAppName,
			Namespace: mgrNs,
		},
		Data: map[string]string{"namespace-selector.yaml": ` 
matchExpressions:
- key: kubernetes.io/metadata.name
  operator: In
  values:
    - default
    - some-ns`},
	}

	podSuffix := "-6699c6cb54-"
	podName := func(name string) string {
		return name + podSuffix
	}
	secretMode := int32(0o644)

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
			Annotations:     map[string]string{agentconfig.InjectAnnotation: "enabled"},
			Labels:          map[string]string{"service": name},
			OwnerReferences: podOwner(name),
			UID:             types.UID(uuid.New().String()),
		}
	}

	podObjectMetaInjected := func(name string, sidecar *agentconfig.Sidecar) meta.ObjectMeta {
		pm := podObjectMeta(name)
		pm.Annotations[agentconfig.ConfigAnnotation] = marshalConfig(t, sidecar)
		pm.Labels[agentconfig.WorkloadNameLabel] = name
		pm.Labels[agentconfig.WorkloadKindLabel] = "Deployment"
		pm.Labels[agentconfig.WorkloadEnabledLabel] = "true"
		return pm
	}

	createClientSet := func() kubernetes.Interface {
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

		return fake.NewClientset(
			&core.Namespace{
				TypeMeta: meta.TypeMeta{
					Kind:       "Namespace",
					APIVersion: "v1",
				},
				ObjectMeta: meta.ObjectMeta{
					Name: "some-ns",
					Labels: map[string]string{
						labels.NameLabelKey: "some-ns",
					},
				},
			},
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
						TargetPort: intstr.FromInt32(8888),
					}},
					Selector: map[string]string{
						"service": "numeric-port",
					},
				},
			},
			&managerConfig,
			&podNamedPort,
			&podNumericPort,
			deployment(&podNamedPort),
			deployment(&podNumericPort),
		)
	}

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
					agentconfig.InjectAnnotation: "enabled",
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
					Containers: []core.Container{
						{
							Name:  "some-container",
							Image: "some-app-image",
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
    - name: AGENT_CONFIG
      valueFrom:
        fieldRef:
          apiVersion: v1
          fieldPath: metadata.annotations['telepresence.getambassador.io/agent-config']
    - name: _TEL_AGENT_POD_IP
      valueFrom:
        fieldRef:
          apiVersion: v1
          fieldPath: status.podIP
    - name: _TEL_AGENT_POD_UID
      valueFrom:
        fieldRef:
          apiVersion: v1
          fieldPath: metadata.uid
    - name: _TEL_AGENT_NAME
      valueFrom:
        fieldRef:
          apiVersion: v1
          fieldPath: metadata.name
    image: ghcr.io/telepresenceio/tel2:2.13.3
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
    - mountPath: /tel_app_exports
      name: export-volume
    - mountPath: /tmp
      name: tel-agent-tmp
- op: replace
  path: /spec/volumes
  value:
  - emptyDir: {}
    name: export-volume
  - emptyDir: {}
    name: tel-agent-tmp
- op: replace
  path: /spec/containers/0/ports/0/name
  value: tm-http
- op: replace
  path: /metadata/annotations
  value:
    telepresence.getambassador.io/agent-config: '%s'
    telepresence.getambassador.io/inject-traffic-agent: enabled
- op: replace
  path: /metadata/labels
  value:
    service: named-port
    telepresence.io/workloadEnabled: "true"
    telepresence.io/workloadKind: Deployment
    telepresence.io/workloadName: named-port
`,
			"",
			nil,
		},
		{
			"Apply Patch: Telepresence API Port",
			&core.Pod{
				ObjectMeta: podObjectMeta("named-port"),
				Spec: core.PodSpec{
					Containers: []core.Container{
						{
							Name:  "some-container",
							Image: "some-app-image",
							Ports: []core.ContainerPort{
								{
									Name: "http", ContainerPort: 8888,
								},
							},
						},
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
    - name: TELEPRESENCE_API_PORT
      value: "9981"
    - name: AGENT_CONFIG
      valueFrom:
        fieldRef:
          apiVersion: v1
          fieldPath: metadata.annotations['telepresence.getambassador.io/agent-config']
    - name: _TEL_AGENT_POD_IP
      valueFrom:
        fieldRef:
          apiVersion: v1
          fieldPath: status.podIP
    - name: _TEL_AGENT_POD_UID
      valueFrom:
        fieldRef:
          apiVersion: v1
          fieldPath: metadata.uid
    - name: _TEL_AGENT_NAME
      valueFrom:
        fieldRef:
          apiVersion: v1
          fieldPath: metadata.name
    image: ghcr.io/telepresenceio/tel2:2.13.3
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
    - mountPath: /tel_app_exports
      name: export-volume
    - mountPath: /tmp
      name: tel-agent-tmp
- op: replace
  path: /spec/volumes
  value:
  - emptyDir: {}
    name: export-volume
  - emptyDir: {}
    name: tel-agent-tmp
- op: replace
  path: /spec/containers/0/ports/0/name
  value: tm-http
- op: replace
  path: /metadata/annotations
  value:
    telepresence.getambassador.io/agent-config: '%s'
    telepresence.getambassador.io/inject-traffic-agent: enabled
- op: replace
  path: /metadata/labels
  value:
    service: named-port
    telepresence.io/workloadEnabled: "true"
    telepresence.io/workloadKind: Deployment
    telepresence.io/workloadName: named-port
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
						agentconfig.InjectAnnotation:      "enabled",
						agentconfig.ServiceNameAnnotation: "khruangbin",
					},
					OwnerReferences: podOwner("named-port"),
				},
				Spec: core.PodSpec{
					Containers: []core.Container{
						{
							Name:  "some-container",
							Image: "some-app-image",
							Ports: []core.ContainerPort{
								{
									Name: "http", ContainerPort: 8888,
								},
							},
						},
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
						agentconfig.InjectAnnotation:      "enabled",
						agentconfig.ServiceNameAnnotation: "named-port",
					},
					OwnerReferences: podOwner("named-port"),
				},
				Spec: core.PodSpec{
					Containers: []core.Container{
						{
							Name:  "some-container",
							Image: "some-app-image",
							Ports: []core.ContainerPort{
								{
									Name: "http", ContainerPort: 8888,
								},
							},
						},
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
    - name: AGENT_CONFIG
      valueFrom:
        fieldRef:
          apiVersion: v1
          fieldPath: metadata.annotations['telepresence.getambassador.io/agent-config']
    - name: _TEL_AGENT_POD_IP
      valueFrom:
        fieldRef:
          apiVersion: v1
          fieldPath: status.podIP
    - name: _TEL_AGENT_POD_UID
      valueFrom:
        fieldRef:
          apiVersion: v1
          fieldPath: metadata.uid
    - name: _TEL_AGENT_NAME
      valueFrom:
        fieldRef:
          apiVersion: v1
          fieldPath: metadata.name
    image: ghcr.io/telepresenceio/tel2:2.13.3
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
    - mountPath: /tel_app_exports
      name: export-volume
    - mountPath: /tmp
      name: tel-agent-tmp
- op: replace
  path: /spec/volumes
  value:
  - emptyDir: {}
    name: export-volume
  - emptyDir: {}
    name: tel-agent-tmp
- op: replace
  path: /spec/containers/0/ports/0/name
  value: tm-http
- op: replace
  path: /metadata/annotations
  value:
    telepresence.getambassador.io/agent-config: '%s'
    telepresence.getambassador.io/inject-service-name: named-port
    telepresence.getambassador.io/inject-traffic-agent: enabled
- op: replace
  path: /metadata/labels
  value:
    service: named-port
    telepresence.io/workloadEnabled: "true"
    telepresence.io/workloadKind: Deployment
    telepresence.io/workloadName: named-port
`,
			"",
			nil,
		},
		{
			"Apply Patch: Numeric port",
			&core.Pod{
				ObjectMeta: podObjectMeta("numeric-port"),
				Spec: core.PodSpec{
					Containers: []core.Container{
						{
							Name:  "some-container",
							Image: "some-app-image",
							Ports: []core.ContainerPort{{ContainerPort: 8888}},
						},
					},
				},
			},
			true,
			`- op: replace
  path: /spec/initContainers
  value:
  - args:
    - agent-init
    env:
    - name: LOG_LEVEL
    - name: AGENT_CONFIG
      valueFrom:
        fieldRef:
          apiVersion: v1
          fieldPath: metadata.annotations['telepresence.getambassador.io/agent-config']
    - name: POD_IP
      valueFrom:
        fieldRef:
          apiVersion: v1
          fieldPath: status.podIP
    image: ghcr.io/telepresenceio/tel2:2.13.3
    name: tel-agent-init
    resources: {}
    securityContext:
      capabilities:
        add:
        - NET_ADMIN
- op: add
  path: /spec/containers/-
  value:
    args:
    - agent
    env:
    - name: AGENT_CONFIG
      valueFrom:
        fieldRef:
          apiVersion: v1
          fieldPath: metadata.annotations['telepresence.getambassador.io/agent-config']
    - name: _TEL_AGENT_POD_IP
      valueFrom:
        fieldRef:
          apiVersion: v1
          fieldPath: status.podIP
    - name: _TEL_AGENT_POD_UID
      valueFrom:
        fieldRef:
          apiVersion: v1
          fieldPath: metadata.uid
    - name: _TEL_AGENT_NAME
      valueFrom:
        fieldRef:
          apiVersion: v1
          fieldPath: metadata.name
    image: ghcr.io/telepresenceio/tel2:2.13.3
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
    - mountPath: /tel_app_exports
      name: export-volume
    - mountPath: /tmp
      name: tel-agent-tmp
- op: replace
  path: /spec/volumes
  value:
  - emptyDir: {}
    name: export-volume
  - emptyDir: {}
    name: tel-agent-tmp
- op: replace
  path: /metadata/annotations
  value:
    telepresence.getambassador.io/agent-config: '%s'
    telepresence.getambassador.io/inject-traffic-agent: enabled
- op: replace
  path: /metadata/labels
  value:
    service: numeric-port
    telepresence.io/workloadEnabled: "true"
    telepresence.io/workloadKind: Deployment
    telepresence.io/workloadName: numeric-port
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
					Containers: []core.Container{
						{
							Name:  "some-container",
							Image: "some-app-image",
							Ports: []core.ContainerPort{{ContainerPort: 8888}},
						},
					},
				},
			},
			true,
			`- op: add
  path: /spec/initContainers/-
  value:
    args:
    - agent-init
    env:
    - name: LOG_LEVEL
    - name: AGENT_CONFIG
      valueFrom:
        fieldRef:
          apiVersion: v1
          fieldPath: metadata.annotations['telepresence.getambassador.io/agent-config']
    - name: POD_IP
      valueFrom:
        fieldRef:
          apiVersion: v1
          fieldPath: status.podIP
    image: ghcr.io/telepresenceio/tel2:2.13.3
    name: tel-agent-init
    resources: {}
    securityContext:
      capabilities:
        add:
        - NET_ADMIN
- op: add
  path: /spec/containers/-
  value:
    args:
    - agent
    env:
    - name: AGENT_CONFIG
      valueFrom:
        fieldRef:
          apiVersion: v1
          fieldPath: metadata.annotations['telepresence.getambassador.io/agent-config']
    - name: _TEL_AGENT_POD_IP
      valueFrom:
        fieldRef:
          apiVersion: v1
          fieldPath: status.podIP
    - name: _TEL_AGENT_POD_UID
      valueFrom:
        fieldRef:
          apiVersion: v1
          fieldPath: metadata.uid
    - name: _TEL_AGENT_NAME
      valueFrom:
        fieldRef:
          apiVersion: v1
          fieldPath: metadata.name
    image: ghcr.io/telepresenceio/tel2:2.13.3
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
    - mountPath: /tel_app_exports
      name: export-volume
    - mountPath: /tmp
      name: tel-agent-tmp
- op: replace
  path: /spec/volumes
  value:
  - emptyDir: {}
    name: export-volume
  - emptyDir: {}
    name: tel-agent-tmp
- op: replace
  path: /metadata/annotations
  value:
    telepresence.getambassador.io/agent-config: '%s'
    telepresence.getambassador.io/inject-traffic-agent: enabled
- op: replace
  path: /metadata/labels
  value:
    service: numeric-port
    telepresence.io/workloadEnabled: "true"
    telepresence.io/workloadKind: Deployment
    telepresence.io/workloadName: numeric-port
`,
			"",
			nil,
		},
		{
			"Apply Patch: re-processing, null patch",
			&core.Pod{
				ObjectMeta: podObjectMetaInjected("numeric-port", &agentconfig.Sidecar{
					AgentImage:   "ghcr.io/telepresenceio/tel2:2.13.3",
					AgentName:    "numeric-port",
					Namespace:    "some-ns",
					WorkloadName: "numeric-port",
					WorkloadKind: "Deployment",
					ManagerHost:  "traffic-manager.default",
					ManagerPort:  8081,
					APIPort:      0,
					Containers: []*agentconfig.Container{
						{
							Name: "some-container",
							Intercepts: []*agentconfig.Intercept{
								{
									ServiceName:       "numeric-port",
									TargetPortNumeric: true,
									Protocol:          "TCP",
									ContainerPort:     8888,
									ServicePort:       80,
									AgentPort:         9900,
								},
							},
							EnvPrefix:  "A_",
							MountPoint: "/tel_app_mounts/some-container",
							Replace:    agentconfig.ReplacePolicyIntercept,
						},
					},
					SecurityContext:     nil,
					InitSecurityContext: nil,
				}),
				Spec: core.PodSpec{
					InitContainers: []core.Container{{
						Name:  agentconfig.InitContainerName,
						Image: "ghcr.io/telepresenceio/tel2:2.13.3",
						Args:  []string{"agent-init"},
						Env: []core.EnvVar{
							{
								Name: "LOG_LEVEL",
							},
							{
								Name: "AGENT_CONFIG",
								ValueFrom: &core.EnvVarSource{
									FieldRef: &core.ObjectFieldSelector{
										APIVersion: "v1",
										FieldPath:  "metadata.annotations['telepresence.getambassador.io/agent-config']",
									},
								},
							},
							{
								Name: "POD_IP",
								ValueFrom: &core.EnvVarSource{
									FieldRef: &core.ObjectFieldSelector{
										APIVersion: "v1",
										FieldPath:  "status.podIP",
									},
								},
							},
						},
						SecurityContext: &core.SecurityContext{
							Capabilities: &core.Capabilities{
								Add: []core.Capability{"NET_ADMIN"},
							},
						},
					}},
					Containers: []core.Container{
						{
							Name:  "some-container",
							Image: "some-app-image",
							Ports: []core.ContainerPort{{ContainerPort: 8888}},
						},
						{
							Name:            agentconfig.ContainerName,
							Image:           "ghcr.io/telepresenceio/tel2:2.13.3",
							ImagePullPolicy: "IfNotPresent",
							Args:            []string{"agent"},
							Ports: []core.ContainerPort{{
								ContainerPort: 9900,
								Protocol:      "TCP",
							}},
							EnvFrom: nil,
							Env: []core.EnvVar{
								{
									Name: "AGENT_CONFIG",
									ValueFrom: &core.EnvVarSource{
										FieldRef: &core.ObjectFieldSelector{
											APIVersion: "v1",
											FieldPath:  "metadata.annotations['telepresence.getambassador.io/agent-config']",
										},
									},
								},
								{
									Name: "_TEL_AGENT_POD_IP",
									ValueFrom: &core.EnvVarSource{
										FieldRef: &core.ObjectFieldSelector{
											APIVersion: "v1",
											FieldPath:  "status.podIP",
										},
									},
								},
								{
									Name: "_TEL_AGENT_POD_UID",
									ValueFrom: &core.EnvVarSource{
										FieldRef: &core.ObjectFieldSelector{
											APIVersion: "v1",
											FieldPath:  "metadata.uid",
										},
									},
								},
								{
									Name: "_TEL_AGENT_NAME",
									ValueFrom: &core.EnvVarSource{
										FieldRef: &core.ObjectFieldSelector{
											APIVersion: "v1",
											FieldPath:  "metadata.name",
										},
									},
								},
							},
							Resources:                core.ResourceRequirements{},
							TerminationMessagePath:   "/dev/termination-log",
							TerminationMessagePolicy: "File",
							VolumeMounts: []core.VolumeMount{
								{
									Name:      agentconfig.ExportsVolumeName,
									MountPath: "/tel_app_exports",
								},
								{
									Name:      agentconfig.TempVolumeName,
									MountPath: "/tmp",
								},
							},
							ReadinessProbe: &core.Probe{
								ProbeHandler: core.ProbeHandler{
									Exec: &core.ExecAction{Command: []string{"/bin/stat", "/tmp/agent/ready"}},
								},
							},
						},
					},
					Volumes: []core.Volume{
						{
							Name: agentconfig.ExportsVolumeName,
							VolumeSource: core.VolumeSource{
								EmptyDir: &core.EmptyDirVolumeSource{},
							},
						},
						{
							Name: agentconfig.TempVolumeName,
							VolumeSource: core.VolumeSource{
								EmptyDir: &core.EmptyDirVolumeSource{},
							},
						},
					},
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
					Containers: []core.Container{
						{
							Name:  "some-container",
							Image: "some-app-image",
							Env: []core.EnvVar{
								{
									Name:  "TOKEN_VOLUME",
									Value: "default-token-vol",
								},
								{
									Name:  "SECRET_NAME",
									Value: "default-secret-name",
								},
								{
									Name:  "BOTH_NAMES",
									Value: "$(TOKEN_VOLUME) and $(SECRET_NAME)",
								},
							},
							Ports: []core.ContainerPort{
								{
									Name: "http", ContainerPort: 8888,
								},
							},
							VolumeMounts: []core.VolumeMount{
								{Name: "$(TOKEN_VOLUME)", ReadOnly: true, MountPath: serviceAccountMountPath},
							},
						},
					},
					Volumes: []core.Volume{
						{
							Name: "default-token-vol",
							VolumeSource: core.VolumeSource{
								Secret: &core.SecretVolumeSource{
									SecretName:  "default-secret-name",
									DefaultMode: &secretMode,
								},
							},
						},
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
    - name: _TEL_APP_A_TOKEN_VOLUME
      value: default-token-vol
    - name: _TEL_APP_A_SECRET_NAME
      value: default-secret-name
    - name: _TEL_APP_A_BOTH_NAMES
      value: $(_TEL_APP_A_TOKEN_VOLUME) and $(_TEL_APP_A_SECRET_NAME)
    - name: AGENT_CONFIG
      valueFrom:
        fieldRef:
          apiVersion: v1
          fieldPath: metadata.annotations['telepresence.getambassador.io/agent-config']
    - name: _TEL_AGENT_POD_IP
      valueFrom:
        fieldRef:
          apiVersion: v1
          fieldPath: status.podIP
    - name: _TEL_AGENT_POD_UID
      valueFrom:
        fieldRef:
          apiVersion: v1
          fieldPath: metadata.uid
    - name: _TEL_AGENT_NAME
      valueFrom:
        fieldRef:
          apiVersion: v1
          fieldPath: metadata.name
    image: ghcr.io/telepresenceio/tel2:2.13.3
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
    - mountPath: /tel_app_mounts/some-container/var/run/secrets/kubernetes.io/serviceaccount
      name: $(_TEL_APP_A_TOKEN_VOLUME)
      readOnly: true
    - mountPath: /tel_app_exports
      name: export-volume
    - mountPath: /tmp
      name: tel-agent-tmp
- op: add
  path: /spec/volumes/-
  value:
    emptyDir: {}
    name: export-volume
- op: add
  path: /spec/volumes/-
  value:
    emptyDir: {}
    name: tel-agent-tmp
- op: replace
  path: /spec/containers/0/ports/0/name
  value: tm-http
- op: replace
  path: /metadata/annotations
  value:
    telepresence.getambassador.io/agent-config: '%s'
    telepresence.getambassador.io/inject-traffic-agent: enabled
- op: replace
  path: /metadata/labels
  value:
    service: named-port
    telepresence.io/workloadEnabled: "true"
    telepresence.io/workloadKind: Deployment
    telepresence.io/workloadName: named-port
`,
			"",
			nil,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := dlog.NewTestContext(t, false)
			env := &managerutil.Env{
				ServerHost: "tel-example",
				ServerPort: 8081,

				ManagerNamespace:  "default",
				AgentRegistry:     "ghcr.io/telepresenceio",
				AgentImageName:    "tel2",
				AgentImageTag:     "2.13.3",
				AgentPort:         9900,
				AgentInjectPolicy: agentconfig.WhenEnabled,

				EnabledWorkloadKinds: k8sapi.Kinds{k8sapi.DeploymentKind, k8sapi.StatefulSetKind, k8sapi.ReplicaSetKind},
			}
			ctx = managerutil.WithEnv(ctx, env)
			if test.envAdditions != nil {
				env := managerutil.GetEnv(ctx)
				newEnv := *env
				ne := reflect.ValueOf(&newEnv).Elem()
				ae := reflect.ValueOf(test.envAdditions).Elem()
				for i := ae.NumField() - 1; i >= 0; i-- {
					ef := ae.Field(i)
					if (ef.Kind() == reflect.String || ef.Kind() == reflect.Uint16) && !ef.IsZero() {
						ne.Field(i).Set(ef)
					}
				}
				ctx = managerutil.WithEnv(ctx, &newEnv)
				agentmap.GeneratorConfigFunc = newEnv.GeneratorConfig
			}
			ctx = setupAgentInjector(t, ctx, createClientSet())
			cw := GetMap(ctx)
			var actualPatch PatchOps
			var actualErr error
			var cfgJSON string
			if test.generateConfig {
				gc, err := agentmap.GeneratorConfigFunc("ghcr.io/telepresenceio/tel2:2.13.3")
				require.NoError(t, err)
				var scx agentconfig.SidecarExt
				if scx, actualErr = generateForPod(t, ctx, test.pod, gc); actualErr == nil {
					cw.Store(scx)
					cfgJSON = marshalConfig(t, scx)
				}
			}
			if actualErr == nil {
				request := toAdmissionRequest(podResource, test.pod)
				a := agentInjector{agentConfigs: cw}
				actualPatch, actualErr = a.Inject(ctx, request)
			}
			requireContains(t, actualErr, strings.ReplaceAll(test.expectedError, "<PODNAME>", test.pod.Name))
			if actualPatch != nil || test.expectedPatch != "" {
				expectedPatch := test.expectedPatch
				if expectedPatch != "null\n" {
					expectedPatch = fmt.Sprintf(expectedPatch, cfgJSON)
				}
				patchBytes, err := json.Marshal(actualPatch, json.Deterministic(true), jsonv1.OmitEmptyWithLegacyDefinition(true), json.FormatNilSliceAsNull(true))
				require.NoError(t, err)
				patchBytes, err = yaml.JSONToYAML(patchBytes)
				require.NoError(t, err)
				patchString := string(patchBytes)
				assert.Equal(t, expectedPatch, patchString, "patches differ")
			}
		})
	}
}

func marshalConfig(t *testing.T, sce agentconfig.SidecarExt) string {
	cfgJSON, err := agentconfig.MarshalTight(sce)
	require.NoError(t, err)
	return cfgJSON
}

func requireContains(t *testing.T, err error, expected string) {
	if expected == "" {
		require.NoError(t, err)
		return
	}
	require.Errorf(t, err, "expected error %q", expected)
	require.Contains(t, err.Error(), expected)
}

func toAdmissionRequest(resource meta.GroupVersionResource, object any) *admission.AdmissionRequest {
	bytes, _ := json.Marshal(object)
	return &admission.AdmissionRequest{
		Resource:  resource,
		Object:    runtime.RawExtension{Raw: bytes},
		Namespace: "default",
	}
}

func generateForPod(t *testing.T, ctx context.Context, pod *core.Pod, gc agentmap.GeneratorConfig) (agentconfig.SidecarExt, error) {
	wl, err := agentmap.FindOwnerWorkload(ctx, k8sapi.Pod(pod), managerutil.GetEnv(ctx).EnabledWorkloadKinds)
	if err != nil {
		return nil, err
	}
	tpl := core.PodTemplateSpec{
		ObjectMeta: pod.ObjectMeta,
		Spec:       pod.Spec,
	}
	switch wi := wl.DeepCopyObject().(type) {
	case *apps.Deployment:
		wi.Spec.Template = tpl
		wl = k8sapi.Deployment(wi)
	case *apps.ReplicaSet:
		wi.Spec.Template = tpl
		wl = k8sapi.ReplicaSet(wi)
	case *apps.StatefulSet:
		wi.Spec.Template = tpl
		wl = k8sapi.StatefulSet(wi)
	default:
		t.Fatalf("bad workload type %T", wi)
	}
	return gc.Generate(ctx, wl, nil)
}

func setupAgentInjector(t *testing.T, ctx context.Context, ci kubernetes.Interface) context.Context {
	env := managerutil.GetEnv(ctx)
	agentmap.GeneratorConfigFunc = env.GeneratorConfig

	ctx = k8sapi.WithJoinedClientSetInterface(ctx, ci, argorolloutsfake.NewSimpleClientset())
	ctx = informer.WithFactory(ctx, "")
	ctx, err := managerutil.WithAgentImageRetriever(ctx, func(context.Context, string) error { return nil })
	require.NoError(t, err)

	configWatcher := config.NewWatcher(mgrNs)
	go func() {
		err := configWatcher.Run(ctx)
		if err != nil {
			t.Error(err)
		}
	}()
	require.NoError(t, configWatcher.ForceEvent(ctx))
	ctx, err = namespaces.InitContext(ctx, configWatcher.SelectorChannel())
	require.NoError(t, err)

	cw := NewWatcher()
	ctx = WithMap(ctx, cw)
	cw.Start(ctx)

	require.NoError(t, cw.StartWatchers(ctx))
	informer.GetK8sFactory(ctx, "").WaitForCacheSync(ctx.Done())
	time.Sleep(time.Second)
	return ctx
}
