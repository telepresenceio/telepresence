package managerutil_test

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/datawire/k8sapi/pkg/k8sapi"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/managerutil"
	"github.com/telepresenceio/telepresence/v2/pkg/agentconfig"
)

func TestEnvconfig(t *testing.T) {
	// Default environment, providing what's necessary for the traffic-manager
	env := map[string]string{
		"REGISTRY":                      "docker.io/datawire",
		"AGENT_APP_PROTO_STRATEGY":      k8sapi.Http2Probe.String(),
		"AGENT_ENVOY_ADMIN_PORT":        "19000",
		"AGENT_ENVOY_SERVER_PORT":       "18000",
		"AGENT_ENVOY_HTTP_IDLE_TIMEOUT": "70s",
		"AGENT_INJECT_POLICY":           agentconfig.OnDemand.String(),
		"AGENT_INJECTOR_NAME":           "agent-injector",
		"AGENT_INJECTOR_SECRET":         "mutator-webhook-tls",
		"AGENT_PORT":                    "9900",
		"AGENT_ARRIVAL_TIMEOUT":         "45s",
		"CLIENT_CONNECTION_TTL":         (24 * time.Hour).String(),
		"CLIENT_DNS_EXCLUDE_SUFFIXES":   ".com .io .net .org .ru",
		"GRPC_MAX_RECEIVE_SIZE":         "4Mi",
		"LOG_LEVEL":                     "info",
		"POD_IP":                        "203.0.113.18",
		"POD_CIDR_STRATEGY":             "auto",
		"SERVER_PORT":                   "8081",
		"INTERCEPT_DISABLE_GLOBAL":      "false",
	}

	defaults := managerutil.Env{
		Registry:                 "docker.io/datawire",
		AgentAppProtocolStrategy: k8sapi.Http2Probe,
		AgentLogLevel:            "info",
		AgentPort:                9900,
		AgentInjectorName:        "agent-injector",
		AgentInjectorSecret:      "mutator-webhook-tls",
		AgentArrivalTimeout:      45 * time.Second,
		ClientConnectionTTL:      24 * time.Hour,
		ClientDnsExcludeSuffixes: []string{".com", ".io", ".net", ".org", ".ru"},
		LogLevel:                 "info",
		MaxReceiveSize:           resource.MustParse("4Mi"),
		PodCIDRStrategy:          "auto",
		PodIP:                    net.IP{203, 0, 113, 18},
		ServerPort:               8081,
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
				"AGENT_REGISTRY": "docker.io/datawire",
			},
			Output: func(e *managerutil.Env) {
				e.AgentRegistry = "docker.io/datawire"
			},
		},
		"complex": {
			Input: map[string]string{
				"CLIENT_ROUTING_NEVER_PROXY_SUBNETS": "10.20.30.0/24 10.20.40.0/24",
			},
			Output: func(e *managerutil.Env) {
				_, a, _ := net.ParseCIDR("10.20.30.0/24")
				_, b, _ := net.ParseCIDR("10.20.40.0/24")
				e.ClientRoutingNeverProxySubnets = []*net.IPNet{a, b}
			},
		},
	}

	for tcName, tc := range testcases {
		tc := tc // Capture loop variable...
		t.Run(tcName, func(t *testing.T) {
			t.Parallel()
			lookup := func(key string) (string, bool) {
				val, ok := tc.Input[key]
				if !ok {
					val, ok = env[key]
				}
				return val, ok
			}

			expected := defaults
			tc.Output(&expected)

			ctx, err := managerutil.LoadEnv(context.Background(), lookup)
			require.NoError(t, err)
			actual := managerutil.GetEnv(ctx)
			assert.Equal(t, &expected, actual)
			assert.Equal(t, "", actual.QualifiedAgentImage())
		})
	}
}
