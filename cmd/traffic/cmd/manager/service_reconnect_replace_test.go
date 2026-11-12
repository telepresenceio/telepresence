package manager

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientfeatures "k8s.io/client-go/features"
	clientfeaturestesting "k8s.io/client-go/features/testing"

	"github.com/telepresenceio/clog/testutil"
	rpc "github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/managerutil"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/mutator"
	testdata "github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/test"
	"github.com/telepresenceio/telepresence/v2/pkg/agentconfig"
	"github.com/telepresenceio/telepresence/v2/pkg/annotation"
	"github.com/telepresenceio/telepresence/v2/pkg/k8sapi"
	"github.com/telepresenceio/telepresence/v2/pkg/tunnel"
)

func replacementReconnectFixture(t *testing.T, noDefaultPort bool, envMods ...func(*managerutil.Env)) (Service, context.Context, *rpc.ReconnectClientRequest) {
	t.Helper()
	clientfeaturestesting.SetFeatureDuringTest(t, clientfeatures.WatchListClient, false)
	ctx := testutil.NewContext(t, true)
	req := require.New(t)
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "test-agent", Namespace: "default"},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "test-agent"}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "test-agent"}},
				Spec: corev1.PodSpec{Containers: []corev1.Container{{
					Name: "app", Ports: []corev1.ContainerPort{{ContainerPort: 8080}},
				}}},
			},
		},
	}
	policy := agentconfig.ReplacePolicyIntercept
	if noDefaultPort {
		policy = agentconfig.ReplacePolicyInactive
	}
	sc := &agentconfig.Sidecar{
		AgentName: "test-agent", Namespace: "default", WorkloadName: "test-agent",
		AgentImage: "test-agent:latest", WorkloadKind: k8sapi.DeploymentKind,
		Containers: []*agentconfig.Container{{
			Name: "app", Replace: policy,
			Intercepts: []*agentconfig.Intercept{{ContainerPort: 8080}},
		}},
	}
	cfgJSON, err := agentconfig.MarshalTight(sc)
	req.NoError(err)
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: "fallback-pod", Namespace: "default", UID: "fallback-pod-uid",
			Labels: map[string]string{
				"app": "test-agent", agentconfig.WorkloadKindLabel: string(k8sapi.DeploymentKind),
				agentconfig.WorkloadNameLabel: "test-agent",
			},
			Annotations: map[string]string{annotation.Config: cfgJSON},
		},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}
	_, mgr, sctx := getTestClientConnAndService(ctx, t, []runtime.Object{dep, pod}, envMods...)
	sctx = managerutil.WithResolvedAgentImageRetriever(sctx, managerutil.ImageFromEnv(sc.AgentImage))
	mutator.GetMap(sctx).Store(sc)
	client := testdata.GetTestClients(t)["alice"]
	return mgr, sctx, &rpc.ReconnectClientRequest{
		Session: &rpc.SessionInfo{SessionId: "expired-session"},
		Client:  client,
		Intercepts: []*rpc.InterceptInfo{{Spec: &rpc.InterceptSpec{
			Name: "test-agent/app", Client: client.Name, Agent: "test-agent", Namespace: "default",
			WorkloadKind: string(k8sapi.DeploymentKind), ContainerName: "app", Mechanism: "tcp",
			Replace: true, NoDefaultPort: noDefaultPort, TargetHost: "127.0.0.1", TargetPort: 3000,
			PodPorts: []string{"8080:3000"},
		}}},
	}
}

func TestReconnectClient_RestoresContainerReplacement(t *testing.T) {
	for _, noDefaultPort := range []bool{true, false} {
		name := "intercept-replace"
		policy := agentconfig.ReplacePolicyIntercept
		if noDefaultPort {
			name = "replace"
			policy = agentconfig.ReplacePolicyInactive
		}
		t.Run(name, func(t *testing.T) {
			req := require.New(t)
			mgr, ctx, reconnect := replacementReconnectFixture(t, noDefaultPort)
			mm := mutator.GetMap(ctx)
			_, err := mgr.ReconnectClient(ctx, reconnect)
			req.NoError(err)
			// Use the same cleanup path as expiration to restore the original app.
			mgr.State().RemoveSession(ctx, tunnel.SessionID(reconnect.Session.SessionId))
			req.Equal(policy, mm.Get("test-agent", "default").Containers[0].Replace)
			req.False(mm.IsInactive("fallback-pod-uid"))

			_, err = mgr.ReconnectClient(ctx, reconnect)
			req.NoError(err)
			stored, ok := mgr.State().GetIntercept("expired-session:test-agent/app")
			req.True(ok)
			req.True(stored.Spec.Replace)
			req.Equal(rpc.InterceptDispositionType_WAITING, stored.Disposition)
			req.Equal(agentconfig.ReplacePolicyContainer, mm.Get("test-agent", "default").Containers[0].Replace)
			req.True(mm.IsInactive("fallback-pod-uid"), "the old pod must not review the restored replacement")

			_, err = mgr.ReconnectClient(ctx, reconnect)
			req.NoError(err, "repeated reconnects are idempotent")
		})
	}
}

func TestReconnectClient_ReplacementFailureCanRetry(t *testing.T) {
	req := require.New(t)
	mgr, ctx, reconnect := replacementReconnectFixture(t, true)
	mm := mutator.GetMap(ctx)
	// Fail after one intercept is restored, so rollback must also run its finalizers.
	bad := &rpc.InterceptInfo{Spec: &rpc.InterceptSpec{
		Name: "missing/app", Client: reconnect.Client.Name, Agent: "missing", Namespace: "default",
		WorkloadKind: string(k8sapi.DeploymentKind), ContainerName: "app", Mechanism: "tcp",
		Replace: true, NoDefaultPort: true, TargetHost: "127.0.0.1", TargetPort: 3001,
	}}
	reconnect.Intercepts = append(reconnect.Intercepts, bad)
	_, err := mgr.ReconnectClient(ctx, reconnect)
	req.ErrorContains(err, "unable to restore replacement")
	req.Nil(mgr.State().GetClient(tunnel.SessionID(reconnect.Session.SessionId)))
	_, ok := mgr.State().GetIntercept("expired-session:test-agent/app")
	req.False(ok)
	req.Equal(agentconfig.ReplacePolicyInactive, mm.Get("test-agent", "default").Containers[0].Replace)

	reconnect.Intercepts = reconnect.Intercepts[:1]
	_, err = mgr.ReconnectClient(ctx, reconnect)
	req.NoError(err)
	req.Equal(agentconfig.ReplacePolicyContainer, mm.Get("test-agent", "default").Containers[0].Replace)
}

func TestReconnectClient_ReplacementSkipsSidecarChanges(t *testing.T) {
	for _, nodeAgent := range []bool{true, false} {
		name := "injector-disabled"
		if nodeAgent {
			name = "node-agent"
		}
		t.Run(name, func(t *testing.T) {
			req := require.New(t)
			mgr, ctx, reconnect := replacementReconnectFixture(t, true, func(env *managerutil.Env) {
				if !nodeAgent {
					env.AgentInjectPolicy = agentconfig.Never
				}
			})
			reconnect.Intercepts[0].Spec.NodeAgent = nodeAgent
			mm := mutator.GetMap(ctx)
			_, err := mgr.ReconnectClient(ctx, reconnect)
			req.NoError(err)
			req.Equal(agentconfig.ReplacePolicyInactive, mm.Get("test-agent", "default").Containers[0].Replace)
			req.False(mm.IsInactive("fallback-pod-uid"))
		})
	}
}
