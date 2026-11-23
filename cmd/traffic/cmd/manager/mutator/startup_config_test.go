package mutator

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apps "k8s.io/api/apps/v1"
	core "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/telepresenceio/clog/testutil"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/managerutil"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/namespaces"
	"github.com/telepresenceio/telepresence/v2/pkg/agentconfig"
	"github.com/telepresenceio/telepresence/v2/pkg/agentmap"
	"github.com/telepresenceio/telepresence/v2/pkg/annotation"
	"github.com/telepresenceio/telepresence/v2/pkg/k8sapi"
	"github.com/telepresenceio/telepresence/v2/pkg/labels"
)

const startupImage = "registry.example/tel2:manager"

func startupDeployment(ns, name string) *apps.Deployment {
	d := stableDeployment(ns, name, 1, nil)
	d.Spec.Template.Annotations = map[string]string{
		annotation.InjectTrafficAgent:   "enabled",
		annotation.InjectContainerPorts: "8080",
	}
	d.Spec.Template.Spec.Containers = []core.Container{{
		Name:  "app",
		Image: "app:1",
		Ports: []core.ContainerPort{{Name: "http", ContainerPort: 8080, Protocol: core.ProtocolTCP}},
	}}
	return d
}

func startupConfigJSON(t *testing.T, sc *agentconfig.Sidecar) string {
	t.Helper()
	s, err := agentconfig.MarshalTight(sc)
	require.NoError(t, err)
	return s
}

// startupFixture runs the startup sequence of the traffic-manager: RegenerateAgentMaps, then SetConfigured.
func startupFixture(t *testing.T, objects ...runtime.Object) *evictionFixture {
	t.Helper()
	f := newEvictionFixture(t, testutil.NewContext(t, false), k8sapi.Kinds{k8sapi.DeploymentKind}, objects...)
	env := managerutil.GetEnv(f.ctx)
	env.AgentPort = 9900
	env.ManagerNamespace = "ambassador"
	env.AgentSecurityContext = &core.SecurityContext{RunAsNonRoot: new(true)}
	selCh := make(chan *labels.Selector, 1)
	selCh <- &labels.Selector{MatchExpressions: []*labels.Requirement{{
		Key:      labels.NameLabelKey,
		Operator: labels.OperatorIn,
		Values:   []string{"a", "b"},
	}}}
	var err error
	f.ctx, err = namespaces.InitContext(f.ctx, selCh)
	require.NoError(t, err)
	require.NoError(t, f.cw.RegenerateAgentMaps(f.ctx, startupImage))
	f.cw.SetConfigured()
	return f
}

func TestStartupIgnoresAnnotationNamingAnotherNamespace(t *testing.T) {
	attacker := startupDeployment("a", "web")
	victim := startupDeployment("b", "victim")
	forged := &agentconfig.Sidecar{
		AgentName:    "victim",
		WorkloadName: "victim",
		WorkloadKind: k8sapi.DeploymentKind,
		Namespace:    "b",
		AgentImage:   "evil.example/agent:1",
	}
	pod := agentPod("a", "web-1", "web", k8sapi.DeploymentKind, startupConfigJSON(t, forged), true)
	f := startupFixture(t, attacker, victim, pod)
	assert.Nil(t, f.cw.Get("victim", "b"))
	assert.Nil(t, f.cw.Get("web", "a"))
}

func TestStartupRebuildsConfigFromManagerValues(t *testing.T) {
	d := startupDeployment("a", "web")
	annotated := &agentconfig.Sidecar{
		AgentName:           "web",
		WorkloadName:        "web",
		WorkloadKind:        k8sapi.DeploymentKind,
		Namespace:           "a",
		AgentImage:          "evil.example/agent:1",
		ManagerHost:         "evil.example",
		SecurityContext:     &core.SecurityContext{Privileged: new(true)},
		InitSecurityContext: &core.SecurityContext{Privileged: new(true)},
		PullSecrets:         []core.LocalObjectReference{{Name: "evil"}},
		Containers:          []*agentconfig.Container{{Name: "app", Replace: agentconfig.ReplacePolicyContainer}},
	}
	pod := agentPod("a", "web-1", "web", k8sapi.DeploymentKind, startupConfigJSON(t, annotated), true)
	f := startupFixture(t, d, pod)

	sc := f.cw.Get("web", "a")
	require.NotNil(t, sc)
	env := managerutil.GetEnv(f.ctx)
	gc, err := env.GeneratorConfig(startupImage)
	require.NoError(t, err)
	assert.Equal(t, startupImage, sc.AgentImage)
	assert.Equal(t, agentmap.ManagerHost(gc.ManagerNamespace, gc.ClusterDomain), sc.ManagerHost)
	assert.Equal(t, env.AgentSecurityContext, sc.SecurityContext)
	assert.Nil(t, sc.InitSecurityContext)
	assert.Empty(t, sc.PullSecrets)
	require.Len(t, sc.Containers, 1)
	assert.Equal(t, agentconfig.ReplacePolicyContainer, sc.Containers[0].Replace)
}

func TestRestoreAgentConfigUsesGeneratedConfig(t *testing.T) {
	d := startupDeployment("a", "web")
	annotated := &agentconfig.Sidecar{
		AgentName:    "web",
		WorkloadName: "web",
		WorkloadKind: k8sapi.DeploymentKind,
		Namespace:    "a",
		AgentImage:   "evil.example/agent:1",
		Containers:   []*agentconfig.Container{{Name: "app", Replace: agentconfig.ReplacePolicyContainer}},
	}
	pod := agentPod("a", "web-1", "web", k8sapi.DeploymentKind, startupConfigJSON(t, annotated), true)
	f := newEvictionFixture(t, testutil.NewContext(t, false), k8sapi.Kinds{k8sapi.DeploymentKind}, d, pod)
	managerutil.GetEnv(f.ctx).AgentPort = 9900
	ctx := WithMap(managerutil.WithResolvedAgentImageRetriever(f.ctx, managerutil.ImageFromEnv(startupImage)), f.cw)

	require.NoError(t, RestoreAgentConfig(ctx, k8sapi.Deployment(d)))
	sc := f.cw.Get("web", "a")
	require.NotNil(t, sc)
	assert.Equal(t, startupImage, sc.AgentImage)
	require.Len(t, sc.Containers, 1)
	assert.Equal(t, agentconfig.ReplacePolicyContainer, sc.Containers[0].Replace)

	// An annotation naming another workload restores nothing.
	f.cw.Delete("web", "a")
	annotated.AgentName, annotated.WorkloadName = "other", "other"
	pod = agentPod("a", "web-1", "web", k8sapi.DeploymentKind, startupConfigJSON(t, annotated), true)
	f.updateStore(pod)
	require.NoError(t, RestoreAgentConfig(ctx, k8sapi.Deployment(d)))
	assert.Nil(t, f.cw.Get("web", "a"))
	assert.Nil(t, f.cw.Get("other", "a"))
}

func TestHandlerPanicIsRecovered(t *testing.T) {
	ctx := testutil.NewContext(t, false)
	assert.NotPanics(t, func() {
		func() {
			defer recoverHandlerPanic(ctx, "test")
			panic("boom")
		}()
	})
}

func TestEvictionWorkerSurvivesPanic(t *testing.T) {
	f := newEvictionFixture(t, testutil.NewContext(t, false), k8sapi.Kinds{k8sapi.DeploymentKind})
	key := WorkloadKey{Name: "web", Namespace: "a", Kind: k8sapi.DeploymentKind}
	state := f.cw.lockEvictionState(key)
	state.requested = true
	state.wanted = &agentconfig.Sidecar{}
	state.Unlock()
	f.cw.evictionStates.Store(key, nil) // makes reconcile dereference a nil state
	_, err := f.cw.reconcileRecovering(f.ctx, key)
	assert.Error(t, err)
}
