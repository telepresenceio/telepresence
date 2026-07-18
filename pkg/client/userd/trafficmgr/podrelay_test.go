package trafficmgr

import (
	"context"
	"errors"
	"fmt"
	"io"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"

	rootdRpc "github.com/telepresenceio/telepresence/rpc/v2/daemon"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
)

func testPod(ip string) *manager.AgentPodInfo {
	return &manager.AgentPodInfo{PodId: "uid", PodName: "n", Namespace: "ns", PodIp: []byte(ip)}
}

// TestPodRelayNextDelta exercises the send-one-delta step (nextDelta /
// commitSent / resetSent) standalone, without a runner or a session.
func TestPodRelayNextDelta(t *testing.T) {
	t.Run("first push after apply is a full reset", func(t *testing.T) {
		r := newPodRelay()
		r.apply(map[string]*manager.AgentPodInfo{"a": testPod("a"), "b": testPod("b")}, nil)

		delta, snapshot, ok := r.nextDelta()
		require.True(t, ok)
		assert.True(t, delta.Reset_)
		assert.Empty(t, delta.Removals)
		assert.Len(t, delta.Upserts, 2)
		r.commitSent(snapshot)

		// Nothing changed: no further delta until something mutates.
		_, _, ok = r.nextDelta()
		assert.False(t, ok)
	})

	t.Run("subsequent apply produces a minimal diff", func(t *testing.T) {
		r := newPodRelay()
		r.apply(map[string]*manager.AgentPodInfo{"a": testPod("a"), "b": testPod("b")}, nil)
		_, snap, _ := r.nextDelta()
		r.commitSent(snap)

		// "a" changes, "b" is dropped, "c" is added.
		r.apply(map[string]*manager.AgentPodInfo{"a": testPod("a-changed"), "c": testPod("c")}, []string{"b"})
		delta, snap2, ok := r.nextDelta()
		require.True(t, ok)
		assert.False(t, delta.Reset_)
		assert.ElementsMatch(t, []string{"b"}, delta.Removals)
		require.Len(t, delta.Upserts, 2)
		assert.Equal(t, []byte("a-changed"), delta.Upserts["a"].PodIp)
		assert.Equal(t, []byte("c"), delta.Upserts["c"].PodIp)
		r.commitSent(snap2)

		_, _, ok = r.nextDelta()
		assert.False(t, ok)
	})

	t.Run("apply feeds legacy deltas incrementally", func(t *testing.T) {
		r := newPodRelay()
		r.apply(map[string]*manager.AgentPodInfo{"a": testPod("a")}, nil)
		delta, snap, ok := r.nextDelta()
		require.True(t, ok)
		assert.True(t, delta.Reset_)
		assert.Len(t, delta.Upserts, 1)
		r.commitSent(snap)

		r.apply(map[string]*manager.AgentPodInfo{"b": testPod("b")}, []string{"a"})
		delta, snap2, ok := r.nextDelta()
		require.True(t, ok)
		assert.False(t, delta.Reset_)
		assert.Equal(t, []string{"a"}, delta.Removals)
		assert.Len(t, delta.Upserts, 1)
		r.commitSent(snap2)
	})

	t.Run("beginSync suppresses pushes until the next mutation", func(t *testing.T) {
		r := newPodRelay()
		r.apply(map[string]*manager.AgentPodInfo{"a": testPod("a")}, nil)
		_, snap, _ := r.nextDelta()
		r.commitSent(snap)

		r.beginSync()
		_, _, ok := r.nextDelta()
		assert.False(t, ok, "nextDelta must not produce anything while syncing")

		r.apply(map[string]*manager.AgentPodInfo{"a": testPod("a")}, nil)
		delta, _, ok := r.nextDelta()
		require.True(t, ok)
		assert.True(t, delta.Reset_, "the push after beginSync must be a full reset")
	})

	t.Run("resetSent forces the next push to be a full reset", func(t *testing.T) {
		r := newPodRelay()
		r.apply(map[string]*manager.AgentPodInfo{"a": testPod("a")}, nil)
		_, snap, _ := r.nextDelta()
		r.commitSent(snap)

		// Simulate a failed send.
		r.resetSent()
		delta, _, ok := r.nextDelta()
		require.True(t, ok)
		assert.True(t, delta.Reset_)
		assert.Len(t, delta.Upserts, 1)
	})
}

// fakeApplierRootDaemon implements the podRelayApplier interface used for the
// in-process root session, and returns Unimplemented (or another error) when
// configured to, to exercise the runner's degradation path.
type fakeApplierRootDaemon struct {
	rootdRpc.DaemonClient // nil: only ApplyAgentPodsDelta is exercised by the runner in this mode
	received              chan *rootdRpc.AgentPodsDelta
	err                   error
}

func (f *fakeApplierRootDaemon) ApplyAgentPodsDelta(_ context.Context, delta *rootdRpc.AgentPodsDelta) error {
	if f.err != nil {
		return f.err
	}
	f.received <- delta
	return nil
}

// testRelayContext returns a context configured with a short watch-retry
// interval, plus a cancel function the caller must invoke (and then wait for
// runErr) before the test returns, so the runner goroutine and its gRPC
// resources don't outlive the test.
func testRelayContext(t *testing.T) (context.Context, context.CancelFunc) {
	cfg := client.GetDefaultConfig()
	cfg.Grpc().WatchRetryInterval = 20 * time.Millisecond
	return context.WithCancel(client.WithConfig(context.Background(), cfg))
}

func TestPodRelayRunInProcess(t *testing.T) {
	ctx, cancel := testRelayContext(t)
	fake := &fakeApplierRootDaemon{received: make(chan *rootdRpc.AgentPodsDelta, 4)}
	s := &session{}
	s.setRootDaemon(fake, nil, nil, false)

	r := newPodRelay()
	runErr := make(chan error, 1)
	go func() { runErr <- r.run(ctx, s) }()

	r.apply(map[string]*manager.AgentPodInfo{"a": testPod("a")}, nil)
	select {
	case delta := <-fake.received:
		assert.True(t, delta.Reset_)
		assert.Len(t, delta.Upserts, 1)
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for relayed delta")
	}

	r.apply(map[string]*manager.AgentPodInfo{"b": testPod("b")}, nil)
	select {
	case delta := <-fake.received:
		assert.False(t, delta.Reset_)
		assert.Len(t, delta.Upserts, 1)
		assert.Contains(t, delta.Upserts, "b")
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for relayed delta")
	}

	select {
	case err := <-runErr:
		t.Fatalf("run returned early: %v", err)
	default:
	}
	cancel()
	<-runErr
}

func TestPodRelayRunInProcessUnimplemented(t *testing.T) {
	ctx, cancel := testRelayContext(t)
	defer cancel()
	fake := &fakeApplierRootDaemon{
		received: make(chan *rootdRpc.AgentPodsDelta, 4),
		err:      status.Error(codes.Unimplemented, "no relay support"),
	}
	s := &session{}
	s.setRootDaemon(fake, nil, nil, false)

	r := newPodRelay()
	runErr := make(chan error, 1)
	go func() { runErr <- r.run(ctx, s) }()

	r.apply(map[string]*manager.AgentPodInfo{"a": testPod("a")}, nil)
	select {
	case err := <-runErr:
		assert.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for run to stop after Unimplemented")
	}
}

// podRelayTestServer is a minimal daemon.Daemon gRPC server that only
// implements WatchAgentPods, used to exercise the runner's client-streaming
// path end-to-end.
type podRelayTestServer struct {
	rootdRpc.UnimplementedDaemonServer
	received      chan *rootdRpc.AgentPodsDelta
	unimplemented bool
}

func (s *podRelayTestServer) WatchAgentPods(stream grpc.ClientStreamingServer[rootdRpc.AgentPodsDelta, emptypb.Empty]) error {
	if s.unimplemented {
		return status.Error(codes.Unimplemented, "not implemented")
	}
	for {
		delta, err := stream.Recv()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return stream.SendAndClose(&emptypb.Empty{})
			}
			return err
		}
		s.received <- delta
	}
}

func TestPodRelayRunStream(t *testing.T) {
	ctx, cancel := testRelayContext(t)
	defer cancel()
	server := &podRelayTestServer{received: make(chan *rootdRpc.AgentPodsDelta, 4)}
	conn, cleanup, err := dialTestRootDaemon(server)
	require.NoError(t, err)
	defer cleanup()

	s := &session{}
	s.setRootDaemon(rootdRpc.NewDaemonClient(conn), conn, nil, false)

	r := newPodRelay()
	runErr := make(chan error, 1)
	go func() { runErr <- r.run(ctx, s) }()

	r.apply(map[string]*manager.AgentPodInfo{"a": testPod("a")}, nil)
	select {
	case delta := <-server.received:
		assert.True(t, delta.Reset_)
		assert.Len(t, delta.Upserts, 1)
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for relayed delta")
	}

	r.apply(map[string]*manager.AgentPodInfo{"b": testPod("b")}, nil)
	select {
	case delta := <-server.received:
		assert.False(t, delta.Reset_)
		assert.Contains(t, delta.Upserts, "b")
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for relayed delta")
	}

	select {
	case err := <-runErr:
		t.Fatalf("run returned early: %v", err)
	default:
	}

	// Cancel and wait for the runner to finish (it does a best-effort
	// CloseAndRecv) before the deferred cleanup tears down the connection and
	// server, so this test doesn't leak a goroutine racing the next test's
	// bufconn setup.
	cancel()
	<-runErr
}

func TestPodRelayRunStreamUnimplemented(t *testing.T) {
	ctx, cancel := testRelayContext(t)
	defer cancel()
	server := &podRelayTestServer{received: make(chan *rootdRpc.AgentPodsDelta, 4), unimplemented: true}
	conn, cleanup, err := dialTestRootDaemon(server)
	require.NoError(t, err)
	defer cleanup()

	s := &session{}
	s.setRootDaemon(rootdRpc.NewDaemonClient(conn), conn, nil, false)

	r := newPodRelay()
	runErr := make(chan error, 1)
	go func() { runErr <- r.run(ctx, s) }()

	// grpc-go's Send can return nil for a message written right after the
	// stream was opened, even though the server already ended the RPC with
	// Unimplemented without reading anything: the local write completes
	// before the client's transport has processed the server's trailers-only
	// response. Keep mutating until the runner observes the failure (it does
	// within a send or two, once that processing catches up), rather than
	// relying on exactly one Send to surface it.
	i := 0
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case err := <-runErr:
			assert.NoError(t, err)
			return
		case <-ticker.C:
			i++
			r.apply(map[string]*manager.AgentPodInfo{"a": testPod(fmt.Sprintf("a%d", i))}, nil)
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for run to stop after Unimplemented")
		}
	}
}
