package agentpf

import (
	"context"
	"net/netip"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/client/k8s"
	"github.com/telepresenceio/telepresence/v2/pkg/maps"
)

// newTestClients returns a *clients backed by a bare k8s.Cluster (no live cluster access,
// exactly as TestClientRefresh_ResetsQuicDead constructs one), suitable for exercising
// ApplyPodsDelta/updateClients without dialing anything -- none of the fixtures below set
// Intercepted, which is what would otherwise trigger a real agent dial.
func newTestClients(ctx context.Context, namespace string) *clients {
	cl := &k8s.Cluster{
		Kubeconfig: &k8s.Kubeconfig{
			Context:   ctx,
			Namespace: namespace,
		},
	}
	cs := NewClients(cl, &manager.SessionInfo{SessionId: "s"}, []string{namespace})
	return cs.(*clients)
}

func podInfo(name, namespace, ip string) *manager.AgentPodInfo {
	addr := netip.MustParseAddr(ip)
	b := addr.As4()
	return &manager.AgentPodInfo{
		PodName:      name,
		Namespace:    namespace,
		PodIp:        b[:],
		WorkloadName: name,
	}
}

func TestApplyPodsDelta_AccumulatesAcrossUpserts(t *testing.T) {
	cs := newTestClients(context.Background(), "ns")

	require.NoError(t, cs.ApplyPodsDelta(false, map[string]*manager.AgentPodInfo{
		"a.ns": podInfo("a", "ns", "10.0.0.1"),
	}, nil))
	require.NoError(t, cs.ApplyPodsDelta(false, map[string]*manager.AgentPodInfo{
		"b.ns": podInfo("b", "ns", "10.0.0.2"),
	}, nil))

	wl, ns, ok := cs.WorkloadForIP(netip.MustParseAddr("10.0.0.1"))
	require.True(t, ok)
	assert.Equal(t, "a", wl)
	assert.Equal(t, "ns", ns)

	wl, ns, ok = cs.WorkloadForIP(netip.MustParseAddr("10.0.0.2"))
	require.True(t, ok)
	assert.Equal(t, "b", wl)
	assert.Equal(t, "ns", ns)
}

func TestApplyPodsDelta_RemovalRemovesOnlyNamedPod(t *testing.T) {
	cs := newTestClients(context.Background(), "ns")

	require.NoError(t, cs.ApplyPodsDelta(true, map[string]*manager.AgentPodInfo{
		"a.ns": podInfo("a", "ns", "10.0.0.1"),
		"b.ns": podInfo("b", "ns", "10.0.0.2"),
	}, nil))
	require.NoError(t, cs.ApplyPodsDelta(false, nil, []string{"a.ns"}))

	_, _, ok := cs.WorkloadForIP(netip.MustParseAddr("10.0.0.1"))
	assert.False(t, ok, "removed pod must be gone")

	wl, ns, ok := cs.WorkloadForIP(netip.MustParseAddr("10.0.0.2"))
	require.True(t, ok, "pod not named in the removal must remain")
	assert.Equal(t, "b", wl)
	assert.Equal(t, "ns", ns)
}

func TestApplyPodsDelta_ResetDiscardsPriorState(t *testing.T) {
	cs := newTestClients(context.Background(), "ns")

	require.NoError(t, cs.ApplyPodsDelta(false, map[string]*manager.AgentPodInfo{
		"a.ns": podInfo("a", "ns", "10.0.0.1"),
	}, nil))
	require.NoError(t, cs.ApplyPodsDelta(true, map[string]*manager.AgentPodInfo{
		"b.ns": podInfo("b", "ns", "10.0.0.2"),
	}, nil))

	_, _, ok := cs.WorkloadForIP(netip.MustParseAddr("10.0.0.1"))
	assert.False(t, ok, "reset must discard pods absent from the reset message")

	wl, ns, ok := cs.WorkloadForIP(netip.MustParseAddr("10.0.0.2"))
	require.True(t, ok)
	assert.Equal(t, "b", wl)
	assert.Equal(t, "ns", ns)
}

// TestApplyPodsDelta_EquivalentToSelfWatch proves that feeding the same logical event
// sequence through ApplyPodsDelta produces the same accumulated state (via updateClients)
// as feeding it through the WatchPods onDelta/onReset callbacks the way the WatchAgentPods
// wrapper wires them (a local snapMap + maps.DeltaUpdate + updateClients). Both sides run
// the real updateClients path; only the entry point differs.
func TestApplyPodsDelta_EquivalentToSelfWatch(t *testing.T) {
	events := []struct {
		reset    bool
		upserts  map[string]*manager.AgentPodInfo
		removals []string
	}{
		{
			reset: true,
			upserts: map[string]*manager.AgentPodInfo{
				"a.ns": podInfo("a", "ns", "10.0.0.1"),
				"b.ns": podInfo("b", "ns", "10.0.0.2"),
			},
		},
		{
			upserts: map[string]*manager.AgentPodInfo{
				"c.ns": podInfo("c", "ns", "10.0.0.3"),
			},
		},
		{
			removals: []string{"a.ns"},
		},
	}

	selfWatch := newTestClients(context.Background(), "ns")
	relay := newTestClients(context.Background(), "ns")

	// Mirrors the composition WatchAgentPods wraps WatchPods with.
	snapMap := make(map[string]*manager.AgentPodInfo)
	onDelta := func(upserts map[string]*manager.AgentPodInfo, removals []string) error {
		maps.DeltaUpdate(snapMap, upserts, removals)
		return selfWatch.updateClients(maps.Values(snapMap))
	}
	onReset := func() error {
		clear(snapMap)
		return nil
	}

	for _, e := range events {
		if e.reset {
			require.NoError(t, onReset())
		}
		require.NoError(t, onDelta(e.upserts, e.removals))
		require.NoError(t, relay.ApplyPodsDelta(e.reset, e.upserts, e.removals))
	}

	assert.Equal(t, selfWatch.snapshot, relay.snapshot)
}

func TestRunDeltaSink_BlocksUntilDoneAndTearsDown(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cs := newTestClients(ctx, "ns")

	done := make(chan error, 1)
	go func() {
		done <- cs.RunDeltaSink(nil)
	}()

	select {
	case err := <-done:
		t.Fatalf("RunDeltaSink returned before the session was done: %v", err)
	case <-time.After(50 * time.Millisecond):
	}

	cancel()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("RunDeltaSink did not return after the session ended")
	}

	assert.True(t, cs.disabled.Load(), "RunDeltaSink must run the same teardown as WatchAgentPods")
}
