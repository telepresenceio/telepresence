package agentpf

import (
	"context"
	"fmt"
	"net/netip"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/client/k8s"
)

func TestWaitForIPUnavailableForUnwatchedNamespace(t *testing.T) {
	cl := &k8s.Cluster{
		Kubeconfig: &k8s.Kubeconfig{
			Context:   context.Background(),
			Namespace: "alpha",
		},
	}
	cs := NewClients(cl, &manager.SessionInfo{SessionId: "session"}, []string{"alpha"}, nil)

	err := cs.WaitForIP(context.Background(), time.Millisecond, "beta", netip.MustParseAddr("10.0.0.1"))
	require.Equal(t, codes.Unavailable, status.Code(err))
}

// TestClients_PreferredQuicAddr proves the default (no callback installed, or a callback
// that hasn't got a winner yet) is "" -- quicEndpointFor's documented fallback to the
// descriptor's own host/port -- and that SetPreferredQuicAddr's callback is consulted on
// every call, not just cached from installation time.
func TestClients_PreferredQuicAddr(t *testing.T) {
	cl := &k8s.Cluster{
		Kubeconfig: &k8s.Kubeconfig{
			Context:   context.Background(),
			Namespace: "alpha",
		},
	}
	cs := NewClients(cl, &manager.SessionInfo{SessionId: "session"}, []string{"alpha"}, nil)
	css, ok := cs.(*clients)
	require.True(t, ok)

	require.Empty(t, css.preferredQuicAddr(), "no callback installed yet")

	winner := ""
	css.SetPreferredQuicAddr(func() string { return winner })
	require.Empty(t, css.preferredQuicAddr(), "callback installed but has no winner yet")

	winner = "198.51.100.5:31778"
	require.Equal(t, winner, css.preferredQuicAddr())

	css.SetPreferredQuicAddr(nil)
	require.Empty(t, css.preferredQuicAddr(), "nil callback reverts to the descriptor fallback")
}

// TestTransportsConcurrentWithRefresh pins the locking contract between Transports
// (which snapshots ac.info under ac.RLock) and refresh (which replaces ac.info under
// ac.Lock). The two race in production -- a status RPC polling Transports while the
// agent watch delivers updates -- so this must be run with -race to catch a
// reintroduction: without the race detector it passes even with the locks removed.
func TestTransportsConcurrentWithRefresh(t *testing.T) {
	cl := &k8s.Cluster{
		Kubeconfig: &k8s.Kubeconfig{
			Context:   context.Background(),
			Namespace: "alpha",
		},
	}
	session := &manager.SessionInfo{SessionId: "session"}
	cs := NewClients(cl, session, []string{"alpha"}, nil)
	css, ok := cs.(*clients)
	require.True(t, ok)

	ac := &client{
		Cluster: cl,
		session: session,
		owner:   css,
		info: &manager.AgentPodInfo{
			PodName:      "echo-1",
			Namespace:    "alpha",
			WorkloadName: "echo",
		},
	}
	ac.transport.Store("quic")
	css.clients.Store("echo-1.alpha", ac)

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := range 1000 {
			ac.refresh(&manager.AgentPodInfo{
				PodName:      fmt.Sprintf("echo-%d", i),
				Namespace:    "alpha",
				WorkloadName: "echo",
			})
		}
	}()
	for range 1000 {
		require.Len(t, cs.Transports(), 1)
	}
	<-done
}

func TestGetRandomAgentSkipsNodeAgent(t *testing.T) {
	cl := &k8s.Cluster{
		Kubeconfig: &k8s.Kubeconfig{
			Context:   context.Background(),
			Namespace: "alpha",
		},
	}
	session := &manager.SessionInfo{SessionId: "session"}
	cs := NewClients(cl, session, []string{"alpha"}, nil)
	css, ok := cs.(*clients)
	require.True(t, ok)

	ai := &manager.AgentPodInfo{
		PodName:   "agent-node",
		Namespace: "ambassador",
		NodeAgent: true,
	}
	css.clients.Store("agent-node.ambassador", &client{
		Cluster: cl,
		session: session,
		owner:   css,
		info:    ai,
	})

	require.Nil(t, cs.GetRandomAgent(context.Background()))
}
