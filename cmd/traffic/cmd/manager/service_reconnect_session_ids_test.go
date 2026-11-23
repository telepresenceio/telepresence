package manager

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	core "k8s.io/api/core/v1"
	k8sErrors "k8s.io/apimachinery/pkg/api/errors"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	rpc "github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/auth"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/config"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/state"
	testdata "github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/test"
	"github.com/telepresenceio/telepresence/v2/pkg/agentconfig"
	"github.com/telepresenceio/telepresence/v2/pkg/k8sapi"
	"github.com/telepresenceio/telepresence/v2/pkg/tunnel"
)

var reconnectModes = []auth.Mode{auth.ModeEnforcing, auth.ModePermissive, auth.ModeDisabled}

// seedAgentPod creates the pod that the agent's verified identity refers to, owned by
// the Deployment that the agent claims.
func seedAgentPod(e *readEnv, agent *rpc.AgentInfo) {
	if agent.Kind == "" {
		agent.Kind = "Deployment"
	}
	if agent.PodIp == "" {
		agent.PodIp = "10.1.2.9"
	}
	pod := &core.Pod{
		ObjectMeta: meta.ObjectMeta{
			Name:      agent.PodName,
			Namespace: agent.Namespace,
			UID:       types.UID(agent.PodUid),
			Labels: map[string]string{
				agentconfig.WorkloadNameLabel: agent.Name,
				agentconfig.WorkloadKindLabel: agent.Kind,
			},
		},
		Spec:   core.PodSpec{Containers: []core.Container{{Name: agentconfig.ContainerName}}},
		Status: core.PodStatus{PodIP: agent.PodIp},
	}
	_, err := k8sapi.GetK8sInterface(e.sctx).CoreV1().Pods(agent.Namespace).Create(e.sctx, pod, meta.CreateOptions{})
	if !k8sErrors.IsAlreadyExists(err) {
		require.NoError(e.t, err)
	}
}

func agentReconnect(e *readEnv, agent *rpc.AgentInfo, sessionID string) (context.Context, *rpc.ReconnectAgentRequest) {
	seedAgentPod(e, agent)
	return auth.WithPrincipal(e.sctx, agentPrincipal(agent)), &rpc.ReconnectAgentRequest{
		Session: &rpc.SessionInfo{SessionId: sessionID},
		Agent:   agent,
	}
}

func TestReconnectAgent_SessionIDMustBelongToAgent(t *testing.T) {
	for _, mode := range reconnectModes {
		t.Run(string(mode), func(t *testing.T) {
			e := newReadEnv(t, mode, isConnectReview)
			agent := proto.Clone(testdata.GetTestAgents(t)["hello"]).(*rpc.AgentInfo)
			otherPod := testdata.GetTestAgents(t)["demo1"]
			st := e.mgr.State()

			// A live client session's ID.
			actx, rq := agentReconnect(e, agent, e.sess.SessionId)
			_, err := e.mgr.ReconnectAgent(actx, rq)
			require.Equal(t, codes.PermissionDenied, status.Code(err))
			require.Equal(t, 0, st.CountAgents())

			// Another pod's agent session ID.
			actx, rq = agentReconnect(e, agent, string(state.AgentSessionID(otherPod.PodUid)))
			_, err = e.mgr.ReconnectAgent(actx, rq)
			require.Equal(t, codes.PermissionDenied, status.Code(err))
			require.Equal(t, 0, st.CountAgents())

			// The client is unaffected.
			_, err = e.mgr.Remain(e.pctx, &rpc.RemainRequest{Session: e.sess})
			require.NoError(t, err)
			require.NotNil(t, st.GetClient(tunnel.SessionID(e.sess.SessionId)))

			// The well-formed reconnect works, also when repeated.
			own := string(state.AgentSessionID(agent.PodUid))
			for range 2 {
				actx, rq = agentReconnect(e, agent, own)
				_, err = e.mgr.ReconnectAgent(actx, rq)
				require.NoError(t, err)
			}
			require.Equal(t, 1, st.CountAgents())
			_, err = e.mgr.Remain(actx, &rpc.RemainRequest{Session: &rpc.SessionInfo{SessionId: own}})
			require.NoError(t, err)

			// The agent's Depart removes the agent only.
			_, err = e.mgr.Depart(actx, &rpc.SessionInfo{SessionId: own})
			require.NoError(t, err)
			require.Eventually(t, func() bool { return st.CountAgents() == 0 }, 5e9, 10e6)
			_, err = e.mgr.Remain(e.pctx, &rpc.RemainRequest{Session: e.sess})
			require.NoError(t, err)
		})
	}
}

func TestReconnectClient_RejectsAgentFormID(t *testing.T) {
	for _, mode := range reconnectModes {
		t.Run(string(mode), func(t *testing.T) {
			e := newReadEnv(t, mode, isConnectReview)
			agent := proto.Clone(testdata.GetTestAgents(t)["hello"]).(*rpc.AgentInfo)
			own := string(state.AgentSessionID(agent.PodUid))
			actx, rq := agentReconnect(e, agent, own)
			_, err := e.mgr.ReconnectAgent(actx, rq)
			require.NoError(t, err)

			for _, id := range []string{own, string(state.AgentSessionID("unknown-pod"))} {
				_, err = e.mgr.ReconnectClient(e.pctx, &rpc.ReconnectClientRequest{
					Session: &rpc.SessionInfo{SessionId: id},
					Client:  testdata.GetTestClients(t)["alice"],
				})
				require.Equal(t, codes.InvalidArgument, status.Code(err))
				require.Nil(t, e.mgr.State().GetClient(tunnel.SessionID(id)))
			}
			require.Equal(t, 1, e.mgr.State().CountClients())
		})
	}
}

type excludingWatcher struct {
	config.Watcher
}

func (excludingWatcher) GetAgentEnv() config.AgentEnv {
	return config.AgentEnv{Excluded: []string{"SECRET"}}
}

func TestReconnectAgent_ValidatesAndStripsExcludedEnv(t *testing.T) {
	for _, mode := range reconnectModes {
		t.Run(string(mode), func(t *testing.T) {
			e := newReadEnv(t, mode, isConnectReview)
			svc, ok := e.mgr.(*service)
			require.True(t, ok)
			svc.configWatcher = excludingWatcher{svc.configWatcher}

			agent := proto.Clone(testdata.GetTestAgents(t)["hello"]).(*rpc.AgentInfo)
			agent.Containers = map[string]*rpc.AgentInfo_ContainerInfo{
				"app": {Environment: map[string]string{"SECRET": "s3", "KEEP": "k"}},
			}
			own := string(state.AgentSessionID(agent.PodUid))
			actx, rq := agentReconnect(e, agent, own)
			_, err := e.mgr.ReconnectAgent(actx, rq)
			require.NoError(t, err)
			stored := e.mgr.State().GetAgent(tunnel.SessionID(own))
			require.NotNil(t, stored)
			env := stored.Containers["app"].Environment
			require.NotContains(t, env, "SECRET")
			require.Equal(t, "k", env["KEEP"])

			unnamed := proto.Clone(agent).(*rpc.AgentInfo)
			unnamed.Name = ""
			unnamed.PodUid = "unnamed-pod"
			actx, rq = agentReconnect(e, unnamed, string(state.AgentSessionID(unnamed.PodUid)))
			_, err = e.mgr.ReconnectAgent(actx, rq)
			require.Equal(t, codes.InvalidArgument, status.Code(err))
			require.Nil(t, e.mgr.State().GetAgent(state.AgentSessionID(unnamed.PodUid)))
		})
	}
}

func TestReconnectClient_ConcurrentSameID(t *testing.T) {
	for _, mode := range reconnectModes {
		t.Run(string(mode), func(t *testing.T) {
			e := newReadEnv(t, mode, isConnectReview)
			before := e.mgr.State().CountClients()
			rq := &rpc.ReconnectClientRequest{
				Session: &rpc.SessionInfo{SessionId: "racing-session"},
				Client:  testdata.GetTestClients(t)["alice"],
			}
			var wg sync.WaitGroup
			errs := make([]error, 16)
			for i := range errs {
				wg.Add(1)
				go func() {
					defer wg.Done()
					_, errs[i] = e.mgr.ReconnectClient(e.pctx, rq)
				}()
			}
			wg.Wait()
			for _, err := range errs {
				require.NoError(t, err)
			}
			require.Equal(t, before+1, e.mgr.State().CountClients())
		})
	}
}

func TestReconnectClient_RequiresSessionID(t *testing.T) {
	for _, mode := range reconnectModes {
		t.Run(string(mode), func(t *testing.T) {
			e := newReadEnv(t, mode, isConnectReview)
			clients := e.mgr.State().CountClients()
			spec := &rpc.InterceptSpec{Name: "web", Client: "alice", Agent: "hello", Namespace: "default", Mechanism: "tcp", TargetPort: 8080}
			for _, session := range []*rpc.SessionInfo{nil, {}} {
				_, err := e.mgr.ReconnectClient(e.pctx, &rpc.ReconnectClientRequest{
					Session:    session,
					Client:     testdata.GetTestClients(t)["alice"],
					Intercepts: []*rpc.InterceptInfo{{Spec: spec}},
				})
				require.Equal(t, codes.InvalidArgument, status.Code(err))
			}
			require.Equal(t, clients, e.mgr.State().CountClients())
			require.Equal(t, 0, e.mgr.State().CountIntercepts())
		})
	}
}
