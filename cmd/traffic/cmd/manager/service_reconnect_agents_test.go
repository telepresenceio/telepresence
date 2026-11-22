package manager

import (
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	rpc "github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/auth"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/state"
	testdata "github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/test"
	"github.com/telepresenceio/telepresence/v2/pkg/tunnel"
)

func TestReconnectClient_ClientSuppliedAgentsAreNotRegistered(t *testing.T) {
	for _, mode := range []auth.Mode{auth.ModeEnforcing, auth.ModePermissive} {
		t.Run(string(mode), func(t *testing.T) {
			e := newReadEnv(t, mode, isConnectReview)

			agent := proto.Clone(testdata.GetTestAgents(t)["hello"]).(*rpc.AgentInfo)
			agent.Namespace = "other"
			agent.PodUid = "invented-pod-uid"
			agent.PodName = "hello-invented"
			agent.PodIp = "10.1.2.9"
			agentID := state.AgentSessionIDPrefix + agent.PodUid

			// A session ID the manager doesn't know, as after a manager restart.
			reconnecting := &rpc.SessionInfo{SessionId: "reconnecting-client-session"}
			_, err := e.mgr.ReconnectClient(e.pctx, &rpc.ReconnectClientRequest{
				Session: reconnecting,
				Client:  testdata.GetTestClients(t)["alice"],
				Agents:  []*rpc.AgentInfo{agent},
			})
			require.NoError(t, err)
			require.NotNil(t, e.mgr.State().GetClient(tunnel.SessionID(reconnecting.SessionId)))

			require.Nil(t, e.mgr.State().GetAgent(tunnel.SessionID(agentID)))

			stream := newFakeServerStream[rpc.InterceptInfoSnapshot](e.pctx)
			err = e.mgr.WatchIntercepts(&rpc.SessionInfo{SessionId: agentID}, stream)
			require.Error(t, err)
			require.Equal(t, codes.NotFound, status.Code(err))
		})
	}
}
