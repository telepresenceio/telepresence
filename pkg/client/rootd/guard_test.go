package rootd

import (
	"context"
	"encoding/json"
	"net"
	"testing"
	"time"

	"github.com/puzpuzpuz/xsync/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"

	rpc "github.com/telepresenceio/telepresence/rpc/v2/daemon"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/daemon"
	"github.com/telepresenceio/telepresence/v2/pkg/client/k8s"
	"github.com/telepresenceio/telepresence/v2/pkg/filelocation"
	"github.com/telepresenceio/telepresence/v2/pkg/types"
)

type guardFixture struct {
	svc *service
	a   rpc.DaemonClient
	b   rpc.DaemonClient
	aCC *grpc.ClientConn
	cfg []byte
}

func newGuardFixture(t *testing.T) *guardFixture {
	t.Helper()
	return newGuardFixtureManaged(t, false)
}

func newGuardFixtureManaged(t *testing.T, managed bool) *guardFixture {
	t.Helper()
	cfg := client.GetDefaultConfig()
	ctx := filelocation.WithAppUserCacheDir(client.WithConfig(t.Context(), cfg), t.TempDir())
	ctx, cancel := context.WithCancel(ctx)
	t.Cleanup(cancel)

	s := newService(cfg, managed)
	s.newSession = func(sessionCtx, _ context.Context, _ *rpc.NetworkConfig, _ chan<- time.Time, _ func(context.Context)) (*session, error) {
		return &session{
			Cluster:   &k8s.Cluster{Kubeconfig: &k8s.Kubeconfig{Context: sessionCtx}},
			l4PortMap: xsync.NewMap[types.AddrPortProto, uint16](),
		}, nil
	}
	s.runSession = func(sn *session, initErrs chan<- error) {
		close(initErrs)
		<-sn.Done()
	}

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	go func() { _ = s.serveGrpc(ctx, func() {}, lis) }()

	dial := func() (rpc.DaemonClient, *grpc.ClientConn) {
		cc, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
		require.NoError(t, err)
		t.Cleanup(func() { _ = cc.Close() })
		return rpc.NewDaemonClient(cc), cc
	}
	f := &guardFixture{svc: s}
	f.a, f.aCC = dial()
	f.b, _ = dial()
	f.cfg, err = json.Marshal(cfg)
	require.NoError(t, err)
	return f
}

func (f *guardFixture) connect(t *testing.T, c rpc.DaemonClient) (*rpc.DaemonStatus, error) {
	t.Helper()
	return c.Connect(t.Context(), &rpc.NetworkConfig{ClientConfig: f.cfg})
}

func (f *guardFixture) hasSession() bool {
	f.svc.sessionLock.RLock()
	defer f.svc.sessionLock.RUnlock()
	return f.svc.session != nil
}

func TestCallerChecks_Session(t *testing.T) {
	f := newGuardFixture(t)
	ctx := t.Context()

	st, err := f.connect(t, f.a)
	require.NoError(t, err)
	require.NotNil(t, st.OutboundConfig)
	require.True(t, f.hasSession())

	t.Run("owner Connect is answered again", func(t *testing.T) {
		st, err := f.connect(t, f.a)
		require.NoError(t, err)
		assert.NotNil(t, st.OutboundConfig)
	})

	t.Run("second Connect is refused", func(t *testing.T) {
		_, err := f.connect(t, f.b)
		assert.True(t, daemon.IsRootDaemonInUse(err), "%v", err)
	})

	t.Run("other connection cannot change or end the session", func(t *testing.T) {
		_, err := f.b.SetDNSTopLevelDomains(ctx, &rpc.Domains{Domains: []string{"x"}})
		assert.True(t, daemon.IsRootDaemonInUse(err), "%v", err)
		_, err = f.b.SetDNSExcludes(ctx, &rpc.SetDNSExcludesRequest{Excludes: []string{"x"}})
		assert.True(t, daemon.IsRootDaemonInUse(err), "%v", err)
		_, err = f.b.SetDNSMappings(ctx, &rpc.SetDNSMappingsRequest{})
		assert.True(t, daemon.IsRootDaemonInUse(err), "%v", err)
		_, err = f.b.GetNetworkConfig(ctx, &emptypb.Empty{})
		assert.True(t, daemon.IsRootDaemonInUse(err), "%v", err)
		_, err = f.b.Disconnect(ctx, &emptypb.Empty{})
		assert.True(t, daemon.IsRootDaemonInUse(err), "%v", err)
		assert.True(t, f.hasSession())
	})

	t.Run("other connection cannot open streams", func(t *testing.T) {
		aw, err := f.b.ActivityWatcher(ctx, &emptypb.Empty{})
		require.NoError(t, err)
		_, err = aw.Recv()
		assert.True(t, daemon.IsRootDaemonInUse(err), "%v", err)

		cs, err := f.b.ProvideCredentials(ctx)
		require.NoError(t, err)
		_, err = cs.Recv()
		assert.True(t, daemon.IsRootDaemonInUse(err), "%v", err)
	})

	t.Run("Version is open to all", func(t *testing.T) {
		_, err := f.b.Version(ctx, &emptypb.Empty{})
		assert.NoError(t, err)
	})

	t.Run("Status", func(t *testing.T) {
		st, err := f.b.Status(ctx, &emptypb.Empty{})
		require.NoError(t, err)
		assert.True(t, st.InUse)
		assert.Nil(t, st.OutboundConfig)
		assert.Nil(t, st.TunnelTransport)
		assert.NotNil(t, st.Version)

		st, err = f.a.Status(ctx, &emptypb.Empty{})
		require.NoError(t, err)
		assert.False(t, st.InUse)
		assert.NotNil(t, st.OutboundConfig)
	})

	t.Run("Quit and SetLogLevel are refused while a session exists", func(t *testing.T) {
		_, err := f.b.Quit(ctx, &emptypb.Empty{})
		assert.True(t, daemon.IsRootDaemonInUse(err), "%v", err)
		_, err = f.b.SetLogLevel(ctx, &manager.LogLevelRequest{LogLevel: "debug"})
		assert.True(t, daemon.IsRootDaemonInUse(err), "%v", err)
		assert.True(t, f.hasSession())
	})

	t.Run("owner may set the log level", func(t *testing.T) {
		_, err := f.a.SetLogLevel(ctx, &manager.LogLevelRequest{LogLevel: "info"})
		assert.NoError(t, err)
	})

	t.Run("closing the owner's connection ends the session", func(t *testing.T) {
		require.NoError(t, f.aCC.Close())
		require.Eventually(t, func() bool { return !f.hasSession() }, 5*time.Second, 10*time.Millisecond)

		st, err := f.b.Status(ctx, &emptypb.Empty{})
		require.NoError(t, err)
		assert.False(t, st.InUse)

		_, err = f.connect(t, f.b)
		require.NoError(t, err)
		assert.True(t, f.hasSession())
	})
}

func TestCallerChecks_Idle(t *testing.T) {
	f := newGuardFixture(t)
	ctx := t.Context()

	st, err := f.b.Status(ctx, &emptypb.Empty{})
	require.NoError(t, err)
	assert.False(t, st.InUse)

	_, err = f.b.SetLogLevel(ctx, &manager.LogLevelRequest{LogLevel: "info"})
	require.NoError(t, err)

	_, err = f.b.GetNetworkConfig(ctx, &emptypb.Empty{})
	require.Error(t, err)
	assert.False(t, daemon.IsRootDaemonInUse(err))

	_, err = f.b.Quit(ctx, &emptypb.Empty{})
	require.NoError(t, err)
}

func TestCallerChecks_OwnerQuit(t *testing.T) {
	f := newGuardFixture(t)
	_, err := f.connect(t, f.a)
	require.NoError(t, err)
	_, err = f.a.Quit(t.Context(), &emptypb.Empty{})
	require.NoError(t, err)
	assert.False(t, f.hasSession())
}

func TestCallerChecks_StreamOpenedWhileIdle(t *testing.T) {
	f := newGuardFixture(t)

	stream, err := f.b.WatchAgentPods(t.Context())
	require.NoError(t, err)
	// Give the server time to start the handler; the guard lets it through while no session exists.
	time.Sleep(200 * time.Millisecond)

	_, err = f.connect(t, f.a)
	require.NoError(t, err)

	require.NoError(t, stream.Send(&rpc.AgentPodsDelta{Reset_: true}))
	_, err = stream.CloseAndRecv()
	assert.True(t, daemon.IsRootDaemonInUse(err), "%v", err)
	assert.True(t, f.hasSession())
}

func TestCallerChecks_QuitRacesConnect(t *testing.T) {
	// holdNewSession makes the next Connect stop inside newSession, where it holds the session lock.
	holdNewSession := func(f *guardFixture) (entered <-chan struct{}, release func()) {
		in := make(chan struct{})
		rel := make(chan struct{})
		orig := f.svc.newSession
		f.svc.newSession = func(sctx, dctx context.Context, nc *rpc.NetworkConfig, ac chan<- time.Time, c func(context.Context)) (*session, error) {
			close(in)
			<-rel
			return orig(sctx, dctx, nc, ac, c)
		}
		return in, func() { close(rel) }
	}

	t.Run("Quit that arrives during Connect is refused", func(t *testing.T) {
		f := newGuardFixture(t)
		entered, release := holdNewSession(f)

		connectErr := make(chan error, 1)
		go func() {
			_, err := f.connect(t, f.a)
			connectErr <- err
		}()
		<-entered

		quitErr := make(chan error, 1)
		go func() {
			_, err := f.b.Quit(t.Context(), &emptypb.Empty{})
			quitErr <- err
		}()
		// The Quit handler needs the lock that Connect holds, so it cannot answer yet.
		select {
		case err := <-quitErr:
			t.Fatalf("Quit was answered while Connect held the lock: %v", err)
		case <-time.After(200 * time.Millisecond):
		}

		release()
		require.NoError(t, <-connectErr)
		assert.True(t, daemon.IsRootDaemonInUse(<-quitErr))
		assert.True(t, f.hasSession())
		st, err := f.a.Status(t.Context(), &emptypb.Empty{})
		require.NoError(t, err)
		assert.NotNil(t, st.OutboundConfig)
	})

	t.Run("Connect that arrives after an accepted Quit fails", func(t *testing.T) {
		f := newGuardFixture(t)
		_, err := f.b.Quit(t.Context(), &emptypb.Empty{})
		require.NoError(t, err)

		_, err = f.connect(t, f.a)
		require.Error(t, err)
		assert.Equal(t, codes.Unavailable, status.Code(err), "%v", err)
		assert.False(t, f.hasSession())
	})

	t.Run("Connect blocked behind a Quit fails", func(t *testing.T) {
		f := newGuardFixture(t)
		f.svc.sessionLock.Lock() // stands in for a Quit that is deciding
		connectErr := make(chan error, 1)
		go func() {
			_, err := f.connect(t, f.a)
			connectErr <- err
		}()
		time.Sleep(100 * time.Millisecond)
		f.svc.quitting = true
		f.svc.sessionLock.Unlock()

		err := <-connectErr
		assert.Equal(t, codes.Unavailable, status.Code(err), "%v", err)
		assert.False(t, f.hasSession())
	})

	t.Run("managed daemon accepts Connect after Quit", func(t *testing.T) {
		f := newGuardFixtureManaged(t, true)
		qr, err := f.b.Quit(t.Context(), &emptypb.Empty{})
		require.NoError(t, err)
		assert.True(t, qr.RootDaemonWillContinue)

		_, err = f.connect(t, f.a)
		require.NoError(t, err)
		assert.True(t, f.hasSession())
	})
}
