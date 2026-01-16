package managerutil_test

import (
	"context"
	"log/slog"
	"net/netip"
	"testing"
	"time"

	"github.com/blang/semver/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/telepresenceio/clog"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/managerutil"
	"github.com/telepresenceio/telepresence/v2/pkg/agentconfig"
	"github.com/telepresenceio/telepresence/v2/pkg/k8sapi"
	"github.com/telepresenceio/telepresence/v2/pkg/maps"
	"github.com/telepresenceio/telepresence/v2/pkg/types"
)

func TestEnvconfig(t *testing.T) {
	// Default environment, providing what's necessary for the traffic-manager
	env := map[string]string{
		"REGISTRY":                        "ghcr.io/telepresenceio",
		"AGENT_ENVOY_ADMIN_PORT":          "19000",
		"AGENT_ENVOY_SERVER_PORT":         "18000",
		"AGENT_ENVOY_HTTP_IDLE_TIMEOUT":   "70s",
		"AGENT_INJECT_POLICY":             agentconfig.WhenEnabled.String(),
		"AGENT_INJECTOR_NAME":             "agent-injector",
		"AGENT_INJECTOR_SECRET":           "mutator-webhook-tls",
		"AGENT_PORT":                      "9900",
		"AGENT_ARRIVAL_TIMEOUT":           "45s",
		"CLIENT_CONNECTION_TTL":           (24 * time.Hour).String(),
		"CLIENT_DNS_EXCLUDE_SUFFIXES":     ".com .io .net .org .ru",
		"ENABLED_WORKLOAD_KINDS":          "Deployment StatefulSet ReplicaSet Rollout",
		"GRPC_MAX_RECEIVE_SIZE":           "4Mi",
		"LOG_LEVEL":                       "trace",
		"POD_IP":                          "203.0.113.18",
		"POD_CIDR_STRATEGY":               "auto",
		"SERVER_PORT":                     "8081",
		"MAX_NAMESPACE_SPECIFIC_WATCHERS": "10",
	}

	defaults := managerutil.Env{
		Registry:                     "ghcr.io/telepresenceio",
		AgentLogLevel:                slog.LevelDebug - 4,
		AgentPort:                    9900,
		AgentInjectorName:            "agent-injector",
		AgentInjectorSecret:          "mutator-webhook-tls",
		AgentInjectPolicy:            agentconfig.WhenEnabled,
		AgentArrivalTimeout:          45 * time.Second,
		ClientConnectionTTL:          24 * time.Hour,
		ClientDnsExcludeSuffixes:     []string{".com", ".io", ".net", ".org", ".ru"},
		LogLevel:                     clog.LevelTrace,
		GrpcMaxReceiveSize:           resource.MustParse("4Mi"),
		PodCidrStrategy:              "auto",
		PodIp:                        netip.AddrFrom4([4]byte{203, 0, 113, 18}),
		ServerPort:                   8081,
		EnabledWorkloadKinds:         []k8sapi.Kind{k8sapi.DeploymentKind, k8sapi.StatefulSetKind, k8sapi.ReplicaSetKind, k8sapi.RolloutKind},
		MaxNamespaceSpecificWatchers: 10,
		AgentInitContainerEnabled:    true,
		InterceptAllowGlobal:         true,
		AgentWatchRetryInterval:      10 * time.Second,
		AgentConsumptionMetrics:      true,
	}

	testcases := map[string]struct {
		Input  map[string]string
		Output func(*managerutil.Env)
	}{
		"empty": {
			Input:  nil,
			Output: func(*managerutil.Env) {},
		},
		"simple": {
			Input: map[string]string{
				"AGENT_REGISTRY": "ghcr.io/telepresenceio",
			},
			Output: func(e *managerutil.Env) {
				e.AgentRegistry = "ghcr.io/telepresenceio"
			},
		},
		"complex": {
			Input: map[string]string{
				"CLIENT_ROUTING_NEVER_PROXY_SUBNETS": "10.20.30.0/24 10.20.40.0/24",
			},
			Output: func(e *managerutil.Env) {
				a := netip.MustParsePrefix("10.20.30.0/24")
				b := netip.MustParsePrefix("10.20.40.0/24")
				e.ClientRoutingNeverProxySubnets = []netip.Prefix{a, b}
			},
		},
		"version": {
			Input: map[string]string{
				"COMPATIBILITY_VERSION": `2.15.3-rc.0`,
			},
			Output: func(e *managerutil.Env) {
				v := semver.MustParse("2.15.3-rc.0")
				e.CompatibilityVersion = &v
			},
		},
		"mountPolicies": {
			Input: map[string]string{
				"AGENT_MOUNT_POLICIES": `{"/home/bob":"remote","/home/alice":"local"}`,
			},
			Output: func(e *managerutil.Env) {
				e.AgentMountPolicies = types.MountPolicies{
					"/home/bob":   types.MountPolicyRemote,
					"/home/alice": types.MountPolicyLocal,
				}
			},
		},
		"resourceRequirements": {
			Input: map[string]string{
				"AGENT_RESOURCES": `{"requests":{"cpu":"100m","memory":"128Mi"},"limits":{"cpu":"200m","memory":"256Mi"}}`,
			},
			Output: func(e *managerutil.Env) {
				e.AgentResources = &corev1.ResourceRequirements{
					Limits: map[corev1.ResourceName]resource.Quantity{
						corev1.ResourceCPU:    resource.MustParse("200m"),
						corev1.ResourceMemory: resource.MustParse("256Mi"),
					},
					Requests: map[corev1.ResourceName]resource.Quantity{
						corev1.ResourceCPU:    resource.MustParse("100m"),
						corev1.ResourceMemory: resource.MustParse("128Mi"),
					},
				}
			},
		},
		"agent-image-pull-secrets": {
			Input: map[string]string{
				"AGENT_IMAGE_PULL_SECRETS": `[{"name":"my-secret"},{"name":"my-other-secret"}]`,
			},
			Output: func(e *managerutil.Env) {
				e.AgentImagePullSecrets = []corev1.LocalObjectReference{
					{
						Name: "my-secret",
					},
					{
						Name: "my-other-secret",
					},
				}
			},
		},
		"securityContext": {
			Input: map[string]string{
				"AGENT_SECURITY_CONTEXT": `{"runAsUser":1000,"runAsGroup":2000}`,
			},
			Output: func(e *managerutil.Env) {
				e.AgentSecurityContext = &corev1.SecurityContext{
					RunAsUser:  func(i int64) *int64 { return &i }(1000),
					RunAsGroup: func(i int64) *int64 { return &i }(2000),
				}
			},
		},
		"allow-global-intercepts-true": {
			Input: map[string]string{
				"INTERCEPT_ALLOW_GLOBAL": "true",
			},
			Output: func(e *managerutil.Env) {
				e.InterceptAllowGlobal = true
			},
		},
		"allow-global-intercepts-false": {
			Input: map[string]string{
				"INTERCEPT_ALLOW_GLOBAL": "false",
			},
			Output: func(e *managerutil.Env) {
				e.InterceptAllowGlobal = false
			},
		},
	}

	for tcName, tc := range testcases {
		t.Run(tcName, func(t *testing.T) {
			t.Parallel()
			testEnv := maps.Copy(env)
			maps.Merge(testEnv, tc.Input)
			expected := defaults
			tc.Output(&expected)

			ctx, err := managerutil.LoadEnv(context.Background(), testEnv)
			require.NoError(t, err)
			actual := managerutil.GetEnv(ctx)
			assert.Equal(t, &expected, actual)
			assert.Equal(t, "", actual.QualifiedAgentImage())
		})
	}
}
