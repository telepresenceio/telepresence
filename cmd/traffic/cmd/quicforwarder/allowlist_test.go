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

	allowlist := NewAllowlist()
	require.False(t, allowlist.Ready())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = WatchAllowlist(ctx, addr, allowlist) }()

	managerIP := netip.MustParseAddr("10.1.1.1")
	agentIP := netip.MustParseAddr("10.2.2.2")
	snapshots <- &rpc.QuicBackendSnapshot{
		Backends: []*rpc.QuicBackend{
			{Ip: managerIP.AsSlice(), Kind: "manager"},
			{Ip: agentIP.AsSlice(), Kind: "agent"},
		},
	}

	waitForCondition(t, 2*time.Second, allowlist.Ready)
	assert.True(t, allowlist.Contains(managerIP))
	assert.True(t, allowlist.Contains(agentIP))
	assert.False(t, allowlist.Contains(netip.MustParseAddr("10.3.3.3")))

	got, ok := allowlist.ManagerAddr()
	require.True(t, ok)
	assert.Equal(t, managerIP, got)
}

func TestWatchAllowlist_ReplacesSnapshotFullyOnUpdate(t *testing.T) {
	addr, snapshots, stop := startFakeManager(t)
	defer stop()

	allowlist := NewAllowlist()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = WatchAllowlist(ctx, addr, allowlist) }()

	first := netip.MustParseAddr("10.1.1.1")
	snapshots <- &rpc.QuicBackendSnapshot{Backends: []*rpc.QuicBackend{{Ip: first.AsSlice(), Kind: "manager"}}}
	waitForCondition(t, 2*time.Second, allowlist.Ready)
	assert.True(t, allowlist.Contains(first))

	second := netip.MustParseAddr("10.9.9.9")
	snapshots <- &rpc.QuicBackendSnapshot{Backends: []*rpc.QuicBackend{{Ip: second.AsSlice(), Kind: "manager"}}}
	waitForCondition(t, 2*time.Second, func() bool { return allowlist.Contains(second) })

	// A snapshot is a full replacement: the previous entry must be gone.
	assert.False(t, allowlist.Contains(first))
}

func TestWatchAllowlist_KeepsLastSnapshotAfterDisconnect(t *testing.T) {
	addr, snapshots, stop := startFakeManager(t)

	allowlist := NewAllowlist()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = WatchAllowlist(ctx, addr, allowlist) }()

	managerIP := netip.MustParseAddr("10.5.5.5")
	snapshots <- &rpc.QuicBackendSnapshot{Backends: []*rpc.QuicBackend{{Ip: managerIP.AsSlice(), Kind: "manager"}}}
	waitForCondition(t, 2*time.Second, allowlist.Ready)

	// Kill the manager entirely; WatchAllowlist will keep retrying (and
	// failing) in the background, but the allowlist must not be cleared.
	stop()
	time.Sleep(50 * time.Millisecond)

	assert.True(t, allowlist.Ready())
	assert.True(t, allowlist.Contains(managerIP))
	got, ok := allowlist.ManagerAddr()
	require.True(t, ok)
	assert.Equal(t, managerIP, got)
}
