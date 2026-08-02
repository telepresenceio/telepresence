package agent

// This file lives in package agent (not agent_test) so the test can reach
// s.(*state).dialWatchers directly: driving the displacement policy end to end requires
// pushing a value through the map's channel and observing which fake server receives it.

import (
	"context"
	"net/netip"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/telepresenceio/clog/testutil"
	agentrpc "github.com/telepresenceio/telepresence/rpc/v2/agent"
	rpc "github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/agentconfig"
	"github.com/telepresenceio/telepresence/v2/pkg/sessiontoken"
	"github.com/telepresenceio/telepresence/v2/pkg/tunnel"
)

// fakeWatchDialServer is a minimal agentrpc.Agent_WatchDialServer: WatchDial only calls
// Context and Send, so embedding a nil grpc.ServerStream satisfies the rest of the
// interface without it ever being invoked.
type fakeWatchDialServer struct {
	grpc.ServerStream
	ctx  context.Context
	sent chan *rpc.DialRequest
}

var _ agentrpc.Agent_WatchDialServer = (*fakeWatchDialServer)(nil)

func newFakeWatchDialServer(ctx context.Context) *fakeWatchDialServer {
	return &fakeWatchDialServer{ctx: ctx, sent: make(chan *rpc.DialRequest, 4)}
}

func (f *fakeWatchDialServer) Context() context.Context { return f.ctx }

func (f *fakeWatchDialServer) Send(dr *rpc.DialRequest) error {
	select {
	case f.sent <- dr:
		return nil
	case <-f.ctx.Done():
		return f.ctx.Err()
	}
}

// TestWatchDial_Displacement drives the real (*state).WatchDial through the
// displacement policy described in "Item 5 hook" in
// docs/plans/auth-hardening/file-sharing-auth.md: an unverified caller may register only
// when no watcher is live for the session; a verified caller always displaces whatever
// was there; and a displaced handler's own exit must not remove its successor's
// registration.
func TestWatchDial_Displacement(t *testing.T) {
	ctx := testutil.NewContext(t, false)
	cfg := &fakeConfig{sidecar: &agentconfig.Sidecar{}, podIP: netip.MustParseAddr("127.0.0.1")}
	st, err := NewState(ctx, cfg)
	require.NoError(t, err)
	s := st.(*state)

	key := generateTestKey(t)
	s.fileShareAuth.set(&key.PublicKey, "permissive")
	validToken, err := sessiontoken.Mint(key, "session-1", time.Now().Add(time.Hour))
	require.NoError(t, err)

	session := &rpc.SessionInfo{SessionId: "session-1"}
	sid := tunnel.SessionID(session.SessionId)

	// First call: unverified (no token in its context), registers because nothing is
	// live yet.
	ctx1, cancel1 := context.WithCancel(ctx)
	server1 := newFakeWatchDialServer(ctx1)
	done1 := make(chan error, 1)
	go func() { done1 <- s.WatchDial(session, server1) }()
	require.Eventually(t, func() bool {
		_, ok := s.dialWatchers.Load(sid)
		return ok
	}, time.Second, time.Millisecond, "first watcher never registered")

	// Second call: also unverified, refused because the first watcher is still live.
	ctx2, cancel2 := context.WithCancel(ctx)
	defer cancel2()
	err = s.WatchDial(session, newFakeWatchDialServer(ctx2))
	require.Equal(t, codes.AlreadyExists, status.Code(err))

	// The first watcher is still live: a dial pushed through the map reaches it.
	ch1, ok := s.dialWatchers.Load(sid)
	require.True(t, ok)
	probe1 := &rpc.DialRequest{ConnId: []byte("probe-1")}
	ch1 <- probe1
	select {
	case got := <-server1.sent:
		require.Same(t, probe1, got)
	case <-time.After(time.Second):
		t.Fatal("first watcher never received the probe")
	}

	// Third call: verified (presents a valid token for this session), always displaces
	// whatever is registered.
	mdCtx := metadata.NewIncomingContext(ctx, metadata.Pairs(sessiontoken.MetadataKey, validToken))
	ctx3, cancel3 := context.WithCancel(mdCtx)
	defer cancel3()
	server3 := newFakeWatchDialServer(ctx3)
	done3 := make(chan error, 1)
	go func() { done3 <- s.WatchDial(session, server3) }()

	var ch3 chan *rpc.DialRequest
	require.Eventually(t, func() bool {
		cur, ok := s.dialWatchers.Load(sid)
		if !ok || cur == ch1 {
			return false
		}
		ch3 = cur
		return true
	}, time.Second, time.Millisecond, "verified call never displaced the existing watcher")

	probe2 := &rpc.DialRequest{ConnId: []byte("probe-2")}
	ch3 <- probe2
	select {
	case got := <-server3.sent:
		require.Same(t, probe2, got)
	case <-time.After(time.Second):
		t.Fatal("displaced-in watcher never received the probe")
	}

	// The first handler's exit must not remove the second's registration: cancel its
	// context, wait for its WatchDial call to return, and confirm the map still
	// resolves the displaced-in channel.
	cancel1()
	select {
	case err := <-done1:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("first WatchDial call never returned after its context was cancelled")
	}

	cur, ok := s.dialWatchers.Load(sid)
	require.True(t, ok)
	require.True(t, cur == ch3, "the displaced-in watcher's registration must survive the replaced handler's exit")

	cancel3()
	select {
	case err := <-done3:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("third WatchDial call never returned after its context was cancelled")
	}
}
