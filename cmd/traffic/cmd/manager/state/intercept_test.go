package state

import (
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/puzpuzpuz/xsync/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apps "k8s.io/api/apps/v1"
	core "k8s.io/api/core/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	clientfeatures "k8s.io/client-go/features"
	clientfeaturestesting "k8s.io/client-go/features/testing"
	"k8s.io/client-go/kubernetes/fake"

	argorolloutsfake "github.com/datawire/argo-rollouts-go-client/pkg/client/clientset/versioned/fake"
	"github.com/telepresenceio/clog"
	"github.com/telepresenceio/clog/testutil"
	rpc "github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/managerutil"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/mutator"
	"github.com/telepresenceio/telepresence/v2/pkg/agentconfig"
	"github.com/telepresenceio/telepresence/v2/pkg/cache"
	"github.com/telepresenceio/telepresence/v2/pkg/informer"
	"github.com/telepresenceio/telepresence/v2/pkg/k8sapi"
	"github.com/telepresenceio/telepresence/v2/pkg/log"
	"github.com/telepresenceio/telepresence/v2/pkg/tunnel"
)

// TestAllowGlobalIntercepts_ValidationLogic tests the validation logic
// for the AllowGlobalIntercepts setting without requiring a full agent setup.
func TestAllowGlobalIntercepts_ValidationLogic(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		allowGlobal      bool
		mechanism        string
		wiretap          bool
		replace          bool
		expectError      bool
		expectedErrorMsg string
	}{
		{
			name:        "global_intercept_allowed_when_enabled",
			allowGlobal: true,
			mechanism:   "tcp",
			wiretap:     false,
			replace:     false,
			expectError: false,
		},
		{
			name:             "global_intercept_blocked_when_disabled",
			allowGlobal:      false,
			mechanism:        "tcp",
			wiretap:          false,
			replace:          false,
			expectError:      true,
			expectedErrorMsg: "global TCP/UDP intercepts and replaces are disabled",
		},
		{
			name:             "replace_blocked_when_disabled",
			allowGlobal:      false,
			mechanism:        "tcp",
			wiretap:          false,
			replace:          true,
			expectError:      true,
			expectedErrorMsg: "global TCP/UDP intercepts and replaces are disabled",
		},
		{
			name:        "replace_allowed_when_enabled",
			allowGlobal: true,
			mechanism:   "tcp",
			wiretap:     false,
			replace:     true,
			expectError: false,
		},
		{
			name:        "http_intercept_allowed_when_global_disabled",
			allowGlobal: false,
			mechanism:   "http",
			wiretap:     false,
			replace:     false,
			expectError: false,
		},
		{
			name:        "wiretap_allowed_when_global_disabled",
			allowGlobal: false,
			mechanism:   "tcp",
			wiretap:     true,
			replace:     false,
			expectError: false,
		},
		{
			name:        "http_intercept_allowed_when_global_enabled",
			allowGlobal: true,
			mechanism:   "http",
			wiretap:     false,
			replace:     false,
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Setup context with appropriate Env
			ctx := testutil.NewContext(t, false)
			env := &managerutil.Env{
				InterceptAllowGlobal: tt.allowGlobal,
			}
			ctx = managerutil.WithEnv(ctx, env)

			// Create a minimal State for testing
			state := &State{
				backgroundCtx:    ctx,
				intercepts:       cache.NewMap[string, *Intercept](interceptEqual, time.Millisecond),
				agents:           cache.NewMap[tunnel.SessionID, *AgentSession](agentsEqual, time.Millisecond),
				clients:          xsync.NewMap[tunnel.SessionID, *ClientSession](),
				workloadWatchers: xsync.NewMap[string, Watcher](),
				timedLogLevel:    log.NewTimedLevel(slog.LevelDebug, clog.SetTreeLevel),
				llSubs:           newLoglevelSubscribers(),
			}

			// Create minimal agent config for testing
			ac := &agentconfig.Sidecar{
				AgentName: "test-agent",
				Namespace: "test-namespace",
			}

			// Create intercept spec
			spec := &rpc.InterceptSpec{
				Mechanism: tt.mechanism,
				Wiretap:   tt.wiretap,
				Replace:   tt.replace,
			}

			// Create minimal CreateInterceptRequest
			cr := &rpc.CreateInterceptRequest{
				InterceptSpec: spec,
			}

			// Create minimal PreparedIntercept
			pi := &rpc.PreparedIntercept{}

			// Call preparePorts which contains our validation logic
			client := &ClientSession{ClientInfo: &rpc.ClientInfo{Name: "client-name"}}
			err := state.checkInterceptConsistency(ac, nil, cr, client, pi)

			// Verify expectations
			if tt.expectError {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.expectedErrorMsg)
			} else if err != nil {
				// preparePorts will fail for other reasons (no actual ports configured),
				// but we should NOT get the "global intercepts disabled" error
				assert.NotContains(t, err.Error(), "global TCP/UDP intercepts and replaces are disabled",
					"Should not fail due to AllowGlobalIntercepts check")
			}
		})
	}
}

// TestAllowGlobalIntercepts_ErrorMessage tests that the error message
// provides helpful guidance to users.
func TestAllowGlobalIntercepts_ErrorMessage(t *testing.T) {
	t.Parallel()

	ctx := testutil.NewContext(t, false)
	env := &managerutil.Env{
		InterceptAllowGlobal: false,
	}
	ctx = managerutil.WithEnv(ctx, env)

	state := &State{
		backgroundCtx:    ctx,
		intercepts:       cache.NewMap[string, *Intercept](interceptEqual, time.Millisecond),
		agents:           cache.NewMap[tunnel.SessionID, *AgentSession](agentsEqual, time.Millisecond),
		clients:          xsync.NewMap[tunnel.SessionID, *ClientSession](),
		workloadWatchers: xsync.NewMap[string, Watcher](),
		timedLogLevel:    log.NewTimedLevel(slog.LevelDebug, clog.SetTreeLevel),
		llSubs:           newLoglevelSubscribers(),
	}

	ac := &agentconfig.Sidecar{
		AgentName: "test-agent",
		Namespace: "test-namespace",
	}

	spec := &rpc.InterceptSpec{
		Mechanism: "tcp", // Global intercept
		Wiretap:   false,
	}

	cr := &rpc.CreateInterceptRequest{
		InterceptSpec: spec,
	}

	pi := &rpc.PreparedIntercept{}

	client := &ClientSession{ClientInfo: &rpc.ClientInfo{Name: "client-name"}}
	err := state.checkInterceptConsistency(ac, nil, cr, client, pi)

	require.Error(t, err)

	errorMsg := err.Error()

	// Verify error message contains key information
	assert.Contains(t, errorMsg, "global TCP/UDP intercepts and replaces are disabled",
		"Error should clearly state that global intercepts and replaces are disabled")

	// Verify error message suggests HTTP header flag
	assert.Contains(t, errorMsg, "--http-header",
		"Error should suggest using --http-header flag")

	// Verify error message suggests HTTP path flags
	assert.True(t,
		strings.Contains(errorMsg, "--http-path-prefix") ||
			strings.Contains(errorMsg, "--http-path-equal") ||
			strings.Contains(errorMsg, "--http-path-regex") ||
			strings.Contains(errorMsg, "--http-path-"),
		"Error should suggest using HTTP path flags")
}

// TestAllowGlobalIntercepts_DefaultBehavior tests that the default
// value (true) is correctly parsed from environment variables for backward compatibility.
func TestAllowGlobalIntercepts_DefaultBehavior(t *testing.T) {
	t.Parallel()

	// Test that the default value is true when loaded from environment
	envMap := map[string]string{
		// Return minimal required environment variables
		"REGISTRY":                    "ghcr.io/telepresenceio",
		"LOG_LEVEL":                   "info",
		"POD_IP":                      "203.0.113.18",
		"POD_CIDR_STRATEGY":           "auto",
		"SERVER_PORT":                 "8081",
		"GRPC_MAX_RECEIVE_SIZE":       "4Mi",
		"CLIENT_DNS_EXCLUDE_SUFFIXES": ".com .io .net .org .ru",
		"CLIENT_CONNECTION_TTL":       "24h",
	}

	ctx := testutil.NewContext(t, false)
	var err error
	ctx, err = managerutil.LoadEnv(ctx, envMap)
	require.NoError(t, err)

	env := managerutil.GetEnv(ctx)

	// Verify default is true (backward compatible) when loaded from environment
	assert.True(t, env.InterceptAllowGlobal,
		"Default value should be true for backward compatibility")

	state := &State{
		backgroundCtx:    ctx,
		intercepts:       cache.NewMap[string, *Intercept](interceptEqual, time.Millisecond),
		agents:           cache.NewMap[tunnel.SessionID, *AgentSession](agentsEqual, time.Millisecond),
		clients:          xsync.NewMap[tunnel.SessionID, *ClientSession](),
		workloadWatchers: xsync.NewMap[string, Watcher](),
		timedLogLevel:    log.NewTimedLevel(slog.LevelDebug, clog.SetTreeLevel),
		llSubs:           newLoglevelSubscribers(),
	}

	ac := &agentconfig.Sidecar{
		AgentName: "test-agent",
		Namespace: "test-namespace",
	}

	spec := &rpc.InterceptSpec{
		Mechanism: "tcp", // Global intercept
		Wiretap:   false,
	}

	cr := &rpc.CreateInterceptRequest{
		InterceptSpec: spec,
	}

	pi := &rpc.PreparedIntercept{}

	client := &ClientSession{ClientInfo: &rpc.ClientInfo{Name: "client-name"}}
	prepErr := state.checkInterceptConsistency(ac, nil, cr, client, pi)

	// Should not fail due to AllowGlobalIntercepts check
	// (may fail for other reasons like missing port config)
	if prepErr != nil {
		assert.NotContains(t, prepErr.Error(), "global TCP/UDP intercepts and replaces are disabled",
			"Default behavior should allow global intercepts and replaces")
	}
}

// TestAgentSessionMatches verifies that waitForAgents' predicate distinguishes
// a node-agent session from a sidecar session for the same workload: a live
// sidecar agent must not satisfy a node-agent wait, and vice versa, even
// though both carry the same name and namespace.
func TestAgentSessionMatches(t *testing.T) {
	t.Parallel()

	const name, namespace = "test-agent", "test-namespace"
	tests := []struct {
		testName      string
		sessionIsNode bool
		wantNode      bool
		want          bool
	}{
		{testName: "sidecar session, sidecar wait", sessionIsNode: false, wantNode: false, want: true},
		{testName: "sidecar session, node-agent wait", sessionIsNode: false, wantNode: true, want: false},
		{testName: "node-agent session, sidecar wait", sessionIsNode: true, wantNode: false, want: false},
		{testName: "node-agent session, node-agent wait", sessionIsNode: true, wantNode: true, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.testName, func(t *testing.T) {
			t.Parallel()
			agent := &AgentSession{AgentInfo: &rpc.AgentInfo{
				Name:      name,
				Namespace: namespace,
				NodeAgent: tt.sessionIsNode,
			}}
			assert.Equal(t, tt.want, agentSessionMatches(agent, name, namespace, tt.wantNode))
		})
	}
}

// TestActiveNodeAgentIntercept verifies the scan PrepareIntercept uses to
// reject a second concurrent node-agent intercept on the same workload, and
// (in the same direction) to reject a sidecar intercept request against a
// workload that already has a live node-agent intercept: it finds a live
// node-agent intercept for the same agent/namespace, ignores the caller's own
// (retried) intercept id, ignores child (pod-port) intercepts, ignores
// intercepts for a different agent or a removed disposition, and ignores
// sidecar intercepts entirely.
func TestActiveNodeAgentIntercept(t *testing.T) {
	t.Parallel()

	state := &State{
		intercepts: cache.NewMap[string, *Intercept](interceptEqual, time.Millisecond),
	}

	spec := &rpc.InterceptSpec{Agent: "test-agent", Namespace: "test-namespace", NodeAgent: true}

	// No intercepts yet.
	assert.Nil(t, state.activeNodeAgentIntercept(spec, "other:ic"))

	// A live node-agent intercept for the same agent is found.
	state.intercepts.Store("c1:ic1", &Intercept{InterceptInfo: &rpc.InterceptInfo{
		Id:          "c1:ic1",
		Disposition: rpc.InterceptDispositionType_ACTIVE,
		Spec:        &rpc.InterceptSpec{Name: "ic1", Client: "user@host1", Agent: "test-agent", Namespace: "test-namespace", NodeAgent: true},
	}})
	found := state.activeNodeAgentIntercept(spec, "other:ic")
	require.NotNil(t, found)
	assert.Equal(t, "c1:ic1", found.Id)

	// The caller's own id (a retried prepare of the same intercept) is excluded.
	assert.Nil(t, state.activeNodeAgentIntercept(spec, "c1:ic1"))

	// A child intercept sharing the parent's spec is ignored.
	state.intercepts.Store("c1:ic1-80-tcp", &Intercept{InterceptInfo: &rpc.InterceptInfo{
		Id:          "c1:ic1-80-tcp",
		Disposition: rpc.InterceptDispositionType_ACTIVE,
		Spec: &rpc.InterceptSpec{
			Name: "ic1-80-tcp", Client: "child 8080:80/tcp ic1 user@host1",
			Agent: "test-agent", Namespace: "test-namespace", NodeAgent: true,
		},
	}})
	found = state.activeNodeAgentIntercept(spec, "other:ic")
	require.NotNil(t, found)
	assert.Equal(t, "c1:ic1", found.Id, "child intercept must not be reported as the conflicting one")

	// A removed intercept is ignored.
	state.intercepts.Store("c1:ic1", &Intercept{InterceptInfo: &rpc.InterceptInfo{
		Id:          "c1:ic1",
		Disposition: rpc.InterceptDispositionType_REMOVED,
		Spec:        &rpc.InterceptSpec{Name: "ic1", Client: "user@host1", Agent: "test-agent", Namespace: "test-namespace", NodeAgent: true},
	}})
	assert.Nil(t, state.activeNodeAgentIntercept(spec, "other:ic"))

	// A sidecar intercept for the same agent is ignored.
	state.intercepts.Store("c2:ic2", &Intercept{InterceptInfo: &rpc.InterceptInfo{
		Id:          "c2:ic2",
		Disposition: rpc.InterceptDispositionType_ACTIVE,
		Spec:        &rpc.InterceptSpec{Name: "ic2", Client: "user@host2", Agent: "test-agent", Namespace: "test-namespace", NodeAgent: false},
	}})
	assert.Nil(t, state.activeNodeAgentIntercept(spec, "other:ic"))

	// A node-agent intercept for a different agent is ignored.
	state.intercepts.Store("c3:ic3", &Intercept{InterceptInfo: &rpc.InterceptInfo{
		Id:          "c3:ic3",
		Disposition: rpc.InterceptDispositionType_ACTIVE,
		Spec:        &rpc.InterceptSpec{Name: "ic3", Client: "user@host3", Agent: "other-agent", Namespace: "test-namespace", NodeAgent: true},
	}})
	assert.Nil(t, state.activeNodeAgentIntercept(spec, "other:ic"))

	// A sidecar-request spec (NodeAgent: false) for the same agent also finds
	// a live node-agent intercept: PrepareIntercept uses this same helper to
	// reject a sidecar intercept that would otherwise inject a traffic-agent
	// and restart the pod the node-agent intercept depends on. c1:ic1 was
	// marked REMOVED above, so re-activate a live node-agent intercept first.
	state.intercepts.Store("c4:ic4", &Intercept{InterceptInfo: &rpc.InterceptInfo{
		Id:          "c4:ic4",
		Disposition: rpc.InterceptDispositionType_ACTIVE,
		Spec:        &rpc.InterceptSpec{Name: "ic4", Client: "user@host4", Agent: "test-agent", Namespace: "test-namespace", NodeAgent: true},
	}})
	sidecarSpec := &rpc.InterceptSpec{Agent: "test-agent", Namespace: "test-namespace", NodeAgent: false}
	found = state.activeNodeAgentIntercept(sidecarSpec, "other:ic")
	require.NotNil(t, found)
	assert.Equal(t, "c4:ic4", found.Id)
}

// TestEnsureAgent_InjectorDisabled verifies that ensureAgent's injector gate
// (the "agent-injector is disabled" rejection) is bypassed for a node-agent
// request -- letting it through to generate an agent config -- while a
// sidecar (non-node-agent) request against the same workload is still
// rejected. This is the manager-side behavior that lets node-agent
// intercepts and wiretaps work with agentInjector.enabled=false.
func TestEnsureAgent_InjectorDisabled(t *testing.T) {
	// The fake clientset doesn't support the WatchListClient feature (no
	// bookmark events), which is enabled by default in client-go v0.35+.
	// Disable it so the informer cache sync below can complete.
	clientfeaturestesting.SetFeatureDuringTest(t, clientfeatures.WatchListClient, false)

	const ns = "default"

	dep := &apps.Deployment{
		ObjectMeta: meta.ObjectMeta{Name: "test-agent", Namespace: ns},
		Spec: apps.DeploymentSpec{
			Selector: &meta.LabelSelector{MatchLabels: map[string]string{"app": "test-agent"}},
			Template: core.PodTemplateSpec{
				ObjectMeta: meta.ObjectMeta{Labels: map[string]string{"app": "test-agent"}},
				Spec: core.PodSpec{
					Containers: []core.Container{{
						Name:  "app",
						Ports: []core.ContainerPort{{ContainerPort: 8080}},
					}},
				},
			},
		},
	}
	svc := &core.Service{
		ObjectMeta: meta.ObjectMeta{Name: "test-agent", Namespace: ns},
		Spec: core.ServiceSpec{
			Selector: map[string]string{"app": "test-agent"},
			Ports: []core.ServicePort{{
				Port:       80,
				TargetPort: intstr.FromInt(8080),
			}},
		},
	}

	ci := fake.NewSimpleClientset(svc)
	ctx := k8sapi.WithJoinedClientSetInterface(t.Context(), ci, argorolloutsfake.NewSimpleClientset())
	ctx = informer.WithFactory(ctx, "")
	f := informer.GetK8sFactory(ctx, "")
	f.Core().V1().Services().Informer()
	f.Start(ctx.Done())
	f.WaitForCacheSync(ctx.Done())

	env := &managerutil.Env{
		ManagerNamespace:  "ambassador",
		NodeAgentEnabled:  true,
		AgentInjectPolicy: agentconfig.Never,
	}
	ctx = managerutil.WithEnv(ctx, env)
	ctx = mutator.WithMap(ctx, mutator.NewWatcher())
	ctx = managerutil.WithResolvedAgentImageRetriever(ctx, managerutil.ImageFromEnv("ghcr.io/telepresenceio/tel2:2.99.0"))

	require.False(t, managerutil.AgentInjectorEnabled(ctx), "test setup must disable the injector")

	wl := k8sapi.Deployment(dep)
	s := &State{}

	t.Run("node-agent spec bypasses the injector gate", func(t *testing.T) {
		spec := &rpc.InterceptSpec{Agent: "test-agent", Namespace: ns, NodeAgent: true}
		sc, _, err := s.ensureAgent(ctx, wl, false, true, spec, agentconfig.ReplacePolicyIntercept)
		require.NoError(t, err)
		require.NotNil(t, sc)
		assert.Equal(t, "ghcr.io/telepresenceio/tel2:2.99.0", sc.AgentImage)
	})

	t.Run("sidecar spec is still rejected", func(t *testing.T) {
		spec := &rpc.InterceptSpec{Agent: "test-agent", Namespace: ns, NodeAgent: false}
		_, _, err := s.ensureAgent(ctx, wl, false, true, spec, agentconfig.ReplacePolicyIntercept)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "agent-injector is disabled")
	})
}
