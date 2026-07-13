package quicforwarder

import (
	"context"
	"net"
	"net/netip"
	"testing"
	"time"

	"google.golang.org/grpc"
	empty "google.golang.org/protobuf/types/known/emptypb"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	rpc "github.com/telepresenceio/telepresence/rpc/v2/manager"
)

// fakeQuicBackendsServer is a minimal rpc.ManagerServer that only implements
// WatchQuicBackends, streaming whatever snapshots are sent on snapshots until ctx is
// done or the channel is closed.
type fakeQuicBackendsServer struct {
	rpc.UnimplementedManagerServer
	snapshots chan *rpc.QuicBackendSnapshot
}

func (s *fakeQuicBackendsServer) WatchQuicBackends(_ *empty.Empty, stream grpc.ServerStreamingServer[rpc.QuicBackendSnapshot]) error {
	for {
		select {
		case <-stream.Context().Done():
			return nil
		case snap, ok := <-s.snapshots:
			if !ok {
				return nil
			}
			if err := stream.Send(snap); err != nil {
				return err
			}
		}
	}
}

func startFakeManager(t *testing.T) (addr string, snapshots chan *rpc.QuicBackendSnapshot, stop func()) {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	snapshots = make(chan *rpc.QuicBackendSnapshot, 4)
	srv := grpc.NewServer()
	rpc.RegisterManagerServer(srv, &fakeQuicBackendsServer{snapshots: snapshots})

	go func() { _ = srv.Serve(lis) }()
	return lis.Addr().String(), snapshots, srv.Stop
}

func waitForCondition(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if cond() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("condition not met within %s", timeout)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestWatchAllowlist_PopulatesFromSnapshot(t *testing.T) {
	addr, snapshots, stop := startFakeManager(t)
	defer stop()

	allowlist := NewAllowlist(0)
	require.False(t, allowlist.Ready())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = WatchAllowlist(ctx, addr, allowlist) }()

	managerIP := netip.MustParseAddr("10.1.1.1")
	agentIP := netip.MustParseAddr("10.2.2.2")
	snapshots <- &rpc.QuicBackendSnapshot{
		Backends: []*rpc.QuicBackend{
			{Ip: managerIP.AsSlice(), Kind: "manager", Port: 7778},
			{Ip: agentIP.AsSlice(), Kind: "agent", Port: 7787, PodUid: "agent-pod-uid"},
		},
	}

	waitForCondition(t, 2*time.Second, allowlist.Ready)
	assert.True(t, allowlist.Contains(managerIP))
	assert.True(t, allowlist.Contains(agentIP))
	assert.False(t, allowlist.Contains(netip.MustParseAddr("10.3.3.3")))

	got, port, ok := allowlist.ManagerBackend()
	require.True(t, ok)
	assert.Equal(t, managerIP, got)
	assert.Equal(t, uint16(7778), port)

	agentGot, agentPort, ok := allowlist.AgentBackend("agent-pod-uid")
	require.True(t, ok)
	assert.Equal(t, agentIP, agentGot)
	assert.Equal(t, uint16(7787), agentPort)
}

func TestWatchAllowlist_ReplacesSnapshotFullyOnUpdate(t *testing.T) {
	addr, snapshots, stop := startFakeManager(t)
	defer stop()

	allowlist := NewAllowlist(0)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = WatchAllowlist(ctx, addr, allowlist) }()

	first := netip.MustParseAddr("10.1.1.1")
	snapshots <- &rpc.QuicBackendSnapshot{Backends: []*rpc.QuicBackend{{Ip: first.AsSlice(), Kind: "manager", Port: 7778}}}
	waitForCondition(t, 2*time.Second, allowlist.Ready)
	assert.True(t, allowlist.Contains(first))

	second := netip.MustParseAddr("10.9.9.9")
	snapshots <- &rpc.QuicBackendSnapshot{Backends: []*rpc.QuicBackend{{Ip: second.AsSlice(), Kind: "manager", Port: 7778}}}
	waitForCondition(t, 2*time.Second, func() bool { return allowlist.Contains(second) })

	// A snapshot is a full replacement: the previous entry must be gone.
	assert.False(t, allowlist.Contains(first))
}

func TestWatchAllowlist_KeepsLastSnapshotAfterDisconnect(t *testing.T) {
	addr, snapshots, stop := startFakeManager(t)

	allowlist := NewAllowlist(0)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = WatchAllowlist(ctx, addr, allowlist) }()

	managerIP := netip.MustParseAddr("10.5.5.5")
	snapshots <- &rpc.QuicBackendSnapshot{Backends: []*rpc.QuicBackend{{Ip: managerIP.AsSlice(), Kind: "manager", Port: 7778}}}
	waitForCondition(t, 2*time.Second, allowlist.Ready)

	// Kill the manager entirely; WatchAllowlist will keep retrying (and
	// failing) in the background, but the allowlist must not be cleared.
	stop()
	time.Sleep(50 * time.Millisecond)

	assert.True(t, allowlist.Ready())
	assert.True(t, allowlist.Contains(managerIP))
	got, port, ok := allowlist.ManagerBackend()
	require.True(t, ok)
	assert.Equal(t, managerIP, got)
	assert.Equal(t, uint16(7778), port)
}

// TestAllowlist_ManagerPortZeroFallsBackToBackendPort proves the "keep BACKEND_PORT
// as a fallback" decision documented on Env.BackendPort: a manager entry reporting
// port 0 (an older traffic-manager that predates QuicBackend.Port) still resolves,
// using the forwarder's configured fallback, instead of being dropped from the
// allowlist outright.
func TestAllowlist_ManagerPortZeroFallsBackToBackendPort(t *testing.T) {
	allowlist := NewAllowlist(7778)
	managerIP := netip.MustParseAddr("10.1.1.1")
	allowlist.update(context.Background(), []*rpc.QuicBackend{{Ip: managerIP.AsSlice(), Kind: "manager"}})

	port, ok := allowlist.Backend(managerIP)
	require.True(t, ok)
	assert.Equal(t, uint16(7778), port)

	got, mgrPort, ok := allowlist.ManagerBackend()
	require.True(t, ok)
	assert.Equal(t, managerIP, got)
	assert.Equal(t, uint16(7778), mgrPort)
}

// TestAllowlist_AgentPortZeroIsDropped proves an agent entry with no reported port
// (no fallback applies to agents -- see Allowlist.fallbackManagerPort) never becomes
// a resolvable backend.
func TestAllowlist_AgentPortZeroIsDropped(t *testing.T) {
	allowlist := NewAllowlist(7778)
	agentIP := netip.MustParseAddr("10.2.2.2")
	allowlist.update(context.Background(), []*rpc.QuicBackend{{Ip: agentIP.AsSlice(), Kind: "agent", PodUid: "some-uid"}})

	assert.False(t, allowlist.Contains(agentIP))
	_, _, ok := allowlist.AgentBackend("some-uid")
	assert.False(t, ok)
}
