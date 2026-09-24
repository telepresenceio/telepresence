package agentpf

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"

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
	assert.Contains(t, err.Error(), "namespace beta is not watched for traffic-agents")
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

// TestGetClientRelayRequiresConnection asserts that an unconnected agent is offered for
// its own pod address but not as a relay for anything else. Relaying is interchangeable
// with the traffic-manager tunnel the caller falls back to, so an agent that has not been
// reached must not be preferred over it.
func TestGetClientRelayRequiresConnection(t *testing.T) {
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

	podIP := netip.MustParseAddr("10.244.0.7")
	css.clients.Store("agent-alpha.alpha", &client{
		Cluster: cl,
		session: session,
		owner:   css,
		info: &manager.AgentPodInfo{
			PodName:   "agent-alpha",
			Namespace: "alpha",
			PodIp:     podIP.AsSlice(),
		},
	})

	// The agent's own pod address still selects it, connected or not.
	require.NotNil(t, cs.GetClient(podIP))

	// A service ClusterIP matches no pod, so the agent would only be a relay.
	require.Nil(t, cs.GetClient(netip.MustParseAddr("10.96.230.123")))
}

// TestGetRandomAgentDoesNotDial asserts that an agent nothing has connected yet is
// skipped rather than dialled. The caller is a name lookup whose deadline is shorter
// than a dial's own timeout, so dialling here would spend the lookup's entire budget
// before it could fall back to the traffic-manager.
func TestGetRandomAgentDoesNotDial(t *testing.T) {
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
			PodName:   "agent-alpha",
			Namespace: "alpha",
		},
	}
	css.clients.Store("agent-alpha.alpha", ac)

	require.Nil(t, cs.GetRandomAgent(context.Background()))

	// A dial would have left its outcome on the client, either a client or an error.
	ac.RLock()
	defer ac.RUnlock()
	require.Nil(t, ac.cli)
	require.NoError(t, ac.connectErr)
}

// --- port-forward denial -----------------------------------------------------------------

// TestIsPortForwardForbidden proves the Forbidden detection works both against a genuine
// *apierrors.StatusError and against the plain-string error the actual port-forward dialers
// (k8s.io/client-go/tools/portforward, k8s.io/streaming/pkg/httpstream/spdy) surface in
// practice: they discard the response's Status object on a refused upgrade and keep only its
// rendered message text.
func TestIsPortForwardForbidden(t *testing.T) {
	forbiddenStatusErr := apierrors.NewForbidden(schema.GroupResource{Resource: "pods"}, "echo-1", errors.New("cannot create resource"))

	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"typed StatusError", forbiddenStatusErr, true},
		{
			"stringified upgrade failure (actual dialer behaviour)",
			errors.New(`error upgrading connection: unable to upgrade connection: pods "echo-1" is forbidden: ` +
				`User "x" cannot create resource "pods/portforward"`),
			true,
		},
		{"unrelated dial error", errors.New("dial tcp 10.0.0.1:10000: connect: connection refused"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, isPortForwardForbidden(tt.err))
		})
	}
}

// TestWrapPortForwardDialer_MarksDeniedOnForbidden exercises the wrapping in isolation, the
// same way TestAgentDialer_* exercises agentDialer: no clients, no network, just the
// closure's own decision.
func TestWrapPortForwardDialer_MarksDeniedOnForbidden(t *testing.T) {
	cl := &k8s.Cluster{Kubeconfig: &k8s.Kubeconfig{Context: context.Background(), Namespace: "alpha"}}
	cs := NewClients(cl, &manager.SessionInfo{SessionId: "s"}, []string{"alpha", "beta"}, nil).(*clients)

	podA := podKey{namespace: "alpha", name: "echo-1"}
	forbidden := apierrors.NewForbidden(schema.GroupResource{Resource: "pods"}, "echo-1", errors.New("denied"))
	dial := wrapPortForwardDialer(cs, podA, func(context.Context, string) (net.Conn, error) {
		return nil, forbidden
	})
	_, err := dial(context.Background(), "addr")
	require.Same(t, forbidden, err, "the original error must be returned unchanged")
	assert.True(t, cs.isPortForwardDenied(podA))
	assert.False(t, cs.isPortForwardDenied(podKey{namespace: "beta", name: "echo-1"}), "only the dialed pod is marked")
	assert.False(t, cs.isPortForwardDenied(podKey{namespace: "alpha", name: "echo-2"}),
		"a different pod in the same namespace is not marked")
}

func TestWrapPortForwardDialer_PlainErrorDoesNotMarkDenied(t *testing.T) {
	cl := &k8s.Cluster{Kubeconfig: &k8s.Kubeconfig{Context: context.Background(), Namespace: "alpha"}}
	cs := NewClients(cl, &manager.SessionInfo{SessionId: "s"}, []string{"alpha"}, nil).(*clients)

	podA := podKey{namespace: "alpha", name: "echo-1"}
	plain := errors.New("dial tcp: connection refused")
	dial := wrapPortForwardDialer(cs, podA, func(context.Context, string) (net.Conn, error) {
		return nil, plain
	})
	_, err := dial(context.Background(), "addr")
	require.Same(t, plain, err)
	assert.False(t, cs.isPortForwardDenied(podA))
}

// TestGetClient_SkipsDeniedPod proves that a denied pod overrides even GetClient's rule 1
// (the agent's own pod address, which otherwise stands whether or not a connection exists
// yet -- see the GetClient doc comment).
func TestGetClient_SkipsDeniedPod(t *testing.T) {
	cl := &k8s.Cluster{Kubeconfig: &k8s.Kubeconfig{Context: context.Background(), Namespace: "alpha"}}
	session := &manager.SessionInfo{SessionId: "session"}
	cs := NewClients(cl, session, []string{"alpha"}, nil).(*clients)

	podIP := netip.MustParseAddr("10.244.0.9")
	cs.clients.Store("agent-alpha.alpha", &client{
		Cluster: cl,
		session: session,
		owner:   cs,
		info: &manager.AgentPodInfo{
			PodName:   "agent-alpha",
			Namespace: "alpha",
			PodIp:     podIP.AsSlice(),
		},
	})

	require.NotNil(t, cs.GetClient(podIP), "sanity: the client is offered before any denial")

	cs.markPortForwardDenied(podKey{namespace: "alpha", name: "agent-alpha"})
	assert.Nil(t, cs.GetClient(podIP), "a denied pod must not be offered, even for its own pod IP")
}

// TestGetClient_DeniedPodDoesNotBlockOtherPodInSameNamespace proves that a denial recorded
// for one pod does not shadow a different pod in the same namespace -- the bug this cache
// exists to avoid, since RBAC can grant pods/portforward per pod via resourceNames.
func TestGetClient_DeniedPodDoesNotBlockOtherPodInSameNamespace(t *testing.T) {
	cl := &k8s.Cluster{Kubeconfig: &k8s.Kubeconfig{Context: context.Background(), Namespace: "alpha"}}
	session := &manager.SessionInfo{SessionId: "session"}
	cs := NewClients(cl, session, []string{"alpha"}, nil).(*clients)

	podIP := netip.MustParseAddr("10.244.0.10")
	cs.clients.Store("agent-b.alpha", &client{
		Cluster: cl,
		session: session,
		owner:   cs,
		info: &manager.AgentPodInfo{
			PodName:   "agent-b",
			Namespace: "alpha",
			PodIp:     podIP.AsSlice(),
		},
	})

	cs.markPortForwardDenied(podKey{namespace: "alpha", name: "agent-a"})
	assert.NotNil(t, cs.GetClient(podIP), "a denial for a different pod in the same namespace must not deny this one")
}

// fakePFDialerFactory returns a pfDialerFactory whose dialer always calls dial and counts
// its invocations in calls, so tests can assert whether a pod was redialed.
func fakePFDialerFactory(
	dial func(ctx context.Context, address string) (net.Conn, error), calls *atomic.Int32,
) func(context.Context) func(context.Context, string) (net.Conn, error) {
	return func(context.Context) func(context.Context, string) (net.Conn, error) {
		return func(ctx context.Context, address string) (net.Conn, error) {
			calls.Add(1)
			return dial(ctx, address)
		}
	}
}

// TestWaitForIP_PortForwardDenied_ShortCircuitsAfterFirstDial drives a real dial (agentDialer
// has no quic_sni to try, so it goes straight to the wrapped port-forward dialer) through a
// fake port-forward dialer that always refuses with Forbidden. WaitForIP must learn the
// refusal from that one dial, return well inside its timeout instead of retrying until the
// deadline, and never dial again for a later call to the same pod.
func TestWaitForIP_PortForwardDenied_ShortCircuitsAfterFirstDial(t *testing.T) {
	old := dialTimeout
	dialTimeout = 50 * time.Millisecond
	t.Cleanup(func() { dialTimeout = old })

	cs := newTestClients(context.Background(), "alpha")
	var calls atomic.Int32
	forbidden := apierrors.NewForbidden(schema.GroupResource{Resource: "pods"}, "echo-1", errors.New("denied"))
	cs.pfDialerFactory = fakePFDialerFactory(func(context.Context, string) (net.Conn, error) {
		return nil, forbidden
	}, &calls)

	ip := netip.MustParseAddr("10.244.0.7")
	ai := &manager.AgentPodInfo{
		PodName:      "echo-1",
		PodId:        "11111111-1111-1111-1111-111111111111",
		Namespace:    "alpha",
		WorkloadName: "echo",
		PodIp:        ip.AsSlice(),
		ApiPort:      10000, // non-zero: a zero port makes resolve() do a live GetPod lookup
	}
	require.NoError(t, cs.ApplyPodsDelta(true, map[string]*manager.AgentPodInfo{"echo-1.alpha": ai}, nil))

	start := time.Now()
	err := cs.WaitForIP(context.Background(), 2*time.Second, "alpha", ip)
	elapsed := time.Since(start)

	require.Equal(t, codes.Unavailable, status.Code(err))
	assert.Contains(t, err.Error(), "direct agent access to pod echo-1.alpha refused (pods/portforward) and the QUIC tunnel is not available")
	assert.Less(t, elapsed, time.Second, "must return well inside the 2s timeout, not exhaust it")
	assert.True(t, cs.isPortForwardDenied(podKey{namespace: "alpha", name: "echo-1", uid: ai.PodId}))

	callsAfterFirst := calls.Load()
	assert.Positive(t, callsAfterFirst, "the fake dialer must have been invoked at least once")

	err = cs.WaitForIP(context.Background(), 2*time.Second, "alpha", ip)
	require.Equal(t, codes.Unavailable, status.Code(err))
	assert.Equal(t, callsAfterFirst, calls.Load(), "a pod already known to be denied must not be redialed")
}

// TestWaitForIP_PortForwardDenied_DoesNotBlockOtherPodInSameNamespace proves the fix for the
// bug this cache exists to guard against: RBAC can grant pods/portforward per pod via
// resourceNames, so a refusal dialing pod A must not deny a dial to pod B in the same
// namespace. Pod A's dial is refused with Forbidden; pod B's dial is left to succeed (no
// error), and WaitForIP for pod B must still reach (and use) the fake dialer instead of
// short-circuiting with errNoDirectAccess.
func TestWaitForIP_PortForwardDenied_DoesNotBlockOtherPodInSameNamespace(t *testing.T) {
	old := dialTimeout
	dialTimeout = 50 * time.Millisecond
	t.Cleanup(func() { dialTimeout = old })

	cs := newTestClients(context.Background(), "alpha")
	var calls atomic.Int32
	forbidden := apierrors.NewForbidden(schema.GroupResource{Resource: "pods"}, "echo-a", errors.New("denied"))
	cs.pfDialerFactory = fakePFDialerFactory(func(context.Context, string) (net.Conn, error) {
		// The first dial is pod A's (refused); every dial after that is pod B's
		// (a plain, unrelated failure -- WaitForIP for B must still reach it).
		if calls.Load() == 1 {
			return nil, forbidden
		}
		return nil, errors.New("dial tcp: connection refused")
	}, &calls)

	ipA := netip.MustParseAddr("10.244.0.11")
	ipB := netip.MustParseAddr("10.244.0.12")
	aiA := &manager.AgentPodInfo{
		PodName:      "echo-a",
		PodId:        "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa",
		Namespace:    "alpha",
		WorkloadName: "echo-a",
		PodIp:        ipA.AsSlice(),
		ApiPort:      10000,
	}
	aiB := &manager.AgentPodInfo{
		PodName:      "echo-b",
		PodId:        "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb",
		Namespace:    "alpha",
		WorkloadName: "echo-b",
		PodIp:        ipB.AsSlice(),
		ApiPort:      10000,
	}
	require.NoError(t, cs.ApplyPodsDelta(true, map[string]*manager.AgentPodInfo{
		"echo-a.alpha": aiA,
		"echo-b.alpha": aiB,
	}, nil))

	err := cs.WaitForIP(context.Background(), 2*time.Second, "alpha", ipA)
	require.Equal(t, codes.Unavailable, status.Code(err))
	assert.True(t, cs.isPortForwardDenied(podKey{namespace: "alpha", name: "echo-a", uid: aiA.PodId}))
	assert.False(t, cs.isPortForwardDenied(podKey{namespace: "alpha", name: "echo-b", uid: aiB.PodId}),
		"pod B must not be marked denied by pod A's refusal")

	callsAfterA := calls.Load()
	err = cs.WaitForIP(context.Background(), 400*time.Millisecond, "alpha", ipB)
	assert.NotEqual(t, codes.Unavailable, status.Code(err),
		"pod B's dial must not short-circuit with errNoDirectAccess just because pod A was denied")
	assert.Greater(t, calls.Load(), callsAfterA, "pod B's dial must actually reach the fake dialer")
}

// TestWaitForIP_PlainDialError_RetriesUntilDeadline is the contrast case: a dial error that
// isn't a Forbidden refusal must not be learned as one, so WaitForIP keeps retrying (and
// redialing) until its own deadline, exactly as before this behaviour existed.
func TestWaitForIP_PlainDialError_RetriesUntilDeadline(t *testing.T) {
	old := dialTimeout
	dialTimeout = 50 * time.Millisecond
	t.Cleanup(func() { dialTimeout = old })

	cs := newTestClients(context.Background(), "alpha")
	var calls atomic.Int32
	cs.pfDialerFactory = fakePFDialerFactory(func(context.Context, string) (net.Conn, error) {
		return nil, errors.New("dial tcp: connection refused")
	}, &calls)

	ip := netip.MustParseAddr("10.244.0.8")
	ai := &manager.AgentPodInfo{
		PodName:      "echo-2",
		PodId:        "22222222-2222-2222-2222-222222222222",
		Namespace:    "alpha",
		WorkloadName: "echo",
		PodIp:        ip.AsSlice(),
		ApiPort:      10000, // non-zero: a zero port makes resolve() do a live GetPod lookup
	}
	require.NoError(t, cs.ApplyPodsDelta(true, map[string]*manager.AgentPodInfo{"echo-2.alpha": ai}, nil))

	err := cs.WaitForIP(context.Background(), 400*time.Millisecond, "alpha", ip)

	assert.False(t, cs.isPortForwardDenied(podKey{namespace: "alpha", name: "echo-2", uid: ai.PodId}),
		"a plain dial error must not be treated as a permission refusal")
	assert.Greater(t, calls.Load(), int32(1), "a plain error must keep retrying (and redialing), not stop after one attempt")
	assert.NotEqual(t, codes.Unavailable, status.Code(err))
}
