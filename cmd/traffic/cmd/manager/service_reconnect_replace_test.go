package manager

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	k8sErrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientfeatures "k8s.io/client-go/features"
	clientfeaturestesting "k8s.io/client-go/features/testing"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/telepresenceio/clog/testutil"
	rpc "github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/managerutil"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/mutator"
	testdata "github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/test"
	"github.com/telepresenceio/telepresence/v2/pkg/agentconfig"
	"github.com/telepresenceio/telepresence/v2/pkg/annotation"
	"github.com/telepresenceio/telepresence/v2/pkg/informer"
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

func TestReconnectClient_PermanentReplacementFailureIsSkipped(t *testing.T) {
	for _, failure := range []string{"missing-workload", "missing-container", "removed-container", "not-interceptable"} {
		t.Run(failure, func(t *testing.T) {
			req := require.New(t)
			mgr, ctx, reconnect := replacementReconnectFixture(t, true)
			bad := proto.Clone(reconnect.Intercepts[0]).(*rpc.InterceptInfo)
			bad.Spec.Name = "invalid/app"
			switch failure {
			case "missing-workload":
				bad.Spec.Agent = "missing"
			case "missing-container":
				bad.Spec.ContainerName = "missing"
			case "removed-container", "not-interceptable":
				cs := k8sapi.GetK8sInterface(ctx)
				dep, err := cs.AppsV1().Deployments("default").Get(ctx, "test-agent", metav1.GetOptions{})
				req.NoError(err)
				dep.Name = "invalid"
				if failure == "removed-container" {
					dep.Spec.Template.Spec.Containers[0].Name = "other"
					// The cached configuration still names the removed container.
					mm := mutator.GetMap(ctx)
					sc := mm.Get("test-agent", "default").Clone()
					sc.AgentName, sc.WorkloadName = dep.Name, dep.Name
					mm.Store(sc)
				} else {
					dep.Spec.Template.Annotations = map[string]string{annotation.InjectTrafficAgent: "disabled"}
				}
				_, err = cs.AppsV1().Deployments("default").Create(ctx, dep, metav1.CreateOptions{})
				req.NoError(err)
				bad.Spec.Agent = dep.Name
			}
			reconnect.Intercepts = append(reconnect.Intercepts, bad)
			_, err := mgr.ReconnectClient(ctx, reconnect)
			req.NoError(err)
			req.NotNil(mgr.State().GetClient(tunnel.SessionID(reconnect.Session.SessionId)))
			_, ok := mgr.State().GetIntercept("expired-session:test-agent/app")
			req.True(ok, "the valid replacement must survive")
			_, ok = mgr.State().GetIntercept("expired-session:invalid/app")
			req.False(ok, "the invalid replacement must be skipped")
			req.Equal(agentconfig.ReplacePolicyContainer, mutator.GetMap(ctx).Get("test-agent", "default").Containers[0].Replace)
		})
	}
}

func TestReconnectClient_TemporaryReplacementFailureCanRetry(t *testing.T) {
	req := require.New(t)
	mgr, ctx, reconnect := replacementReconnectFixture(t, true)
	mm := mutator.GetMap(ctx)
	cs := k8sapi.GetK8sInterface(ctx).(*fake.Clientset)
	dep, err := cs.AppsV1().Deployments("default").Get(ctx, "test-agent", metav1.GetOptions{})
	req.NoError(err)
	dep.Name = "recovering"
	sc := mm.Get("test-agent", "default").Clone()
	sc.AgentName, sc.WorkloadName = dep.Name, dep.Name
	mm.Store(sc)
	unavailable := true
	cs.PrependReactor("get", "deployments", func(action k8stesting.Action) (bool, runtime.Object, error) {
		if action.(k8stesting.GetAction).GetName() != dep.Name {
			return false, nil, nil
		}
		if unavailable {
			return true, nil, k8sErrors.NewServiceUnavailable("API temporarily unavailable")
		}
		return true, dep, nil
	})
	later := proto.Clone(reconnect.Intercepts[0]).(*rpc.InterceptInfo)
	later.Spec.Name, later.Spec.Agent = "recovering/app", dep.Name
	reconnect.Intercepts = append(reconnect.Intercepts, later)
	_, err = mgr.ReconnectClient(ctx, reconnect)
	req.Equal(codes.Unavailable, status.Code(err))
	req.Nil(mgr.State().GetClient(tunnel.SessionID(reconnect.Session.SessionId)))
	_, ok := mgr.State().GetIntercept("expired-session:test-agent/app")
	req.False(ok, "a failed reconnect must clean up partial restoration")
	req.Equal(agentconfig.ReplacePolicyInactive, mm.Get("test-agent", "default").Containers[0].Replace)

	unavailable = false
	_, err = mgr.ReconnectClient(ctx, reconnect)
	req.NoError(err, "the unchanged reconnect request must succeed after API recovery")
	for _, name := range []string{"test-agent", "recovering"} {
		_, ok := mgr.State().GetIntercept("expired-session:" + name + "/app")
		req.True(ok)
		req.Equal(agentconfig.ReplacePolicyContainer, mm.Get(name, "default").Containers[0].Replace)
	}
}

func TestReconnectClient_EmptyConfigMapPreservesActivePod(t *testing.T) {
	req := require.New(t)
	mgr, ctx, reconnect := replacementReconnectFixture(t, true)
	mm := mutator.GetMap(ctx)
	sc := mm.Get("test-agent", "default").Clone()
	sc.Containers[0].Replace = agentconfig.ReplacePolicyContainer
	// Preserve a port assignment that fresh generation does not reproduce.
	sc.Containers[0].Intercepts[0].AgentPort = 12345
	cfgJSON, err := agentconfig.MarshalTight(sc)
	req.NoError(err)
	cs := k8sapi.GetK8sInterface(ctx)
	pod, err := cs.CoreV1().Pods("default").Get(ctx, "fallback-pod", metav1.GetOptions{})
	req.NoError(err)
	pod.Annotations[annotation.Config] = cfgJSON
	pod, err = cs.CoreV1().Pods("default").Update(ctx, pod, metav1.UpdateOptions{})
	req.NoError(err)
	req.NoError(informer.GetK8sFactory(ctx, "default").Core().V1().Pods().Informer().GetStore().Update(pod))
	mm.Delete("test-agent", "default")

	_, err = mgr.ReconnectClient(ctx, reconnect)
	req.NoError(err)
	restored, err := agentconfig.MarshalTight(mm.Get("test-agent", "default"))
	req.NoError(err)
	req.JSONEq(cfgJSON, restored)
	req.False(mm.IsInactive(pod.UID), "a healthy replacement pod must remain available after manager restart")
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
