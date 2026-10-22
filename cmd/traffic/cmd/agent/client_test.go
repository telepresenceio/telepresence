package agent

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/telepresenceio/clog/testutil"
	rpc "github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/quictunnel"
	"github.com/telepresenceio/telepresence/v2/pkg/dos"
	"github.com/telepresenceio/telepresence/v2/pkg/dos/aferofs"
	"github.com/telepresenceio/telepresence/v2/pkg/quicfwd"
	"github.com/telepresenceio/telepresence/v2/pkg/sessiontoken"
	"github.com/telepresenceio/telepresence/v2/pkg/tunnel"
)

type handshakeManagerClient struct {
	rpc.ManagerClient
	version func(context.Context) (*rpc.VersionInfo2, error)
	arrive  func(context.Context) (*rpc.SessionInfo, error)
}

func (c handshakeManagerClient) Version(ctx context.Context, _ *emptypb.Empty, _ ...grpc.CallOption) (*rpc.VersionInfo2, error) {
	return c.version(ctx)
}

func (c handshakeManagerClient) ArriveAsAgent(ctx context.Context, _ *rpc.AgentInfo, _ ...grpc.CallOption) (*rpc.SessionInfo, error) {
	return c.arrive(ctx)
}

func TestTalkToManagerTimesOutHandshakeRPCs(t *testing.T) {
	oldTimeout := managerHandshakeTimeout
	managerHandshakeTimeout = 10 * time.Millisecond
	t.Cleanup(func() { managerHandshakeTimeout = oldTimeout })

	tests := []struct {
		name   string
		client handshakeManagerClient
	}{
		{
			name: "version",
			client: handshakeManagerClient{
				version: func(ctx context.Context) (*rpc.VersionInfo2, error) {
					<-ctx.Done()
					return nil, ctx.Err()
				},
				arrive: func(context.Context) (*rpc.SessionInfo, error) {
					t.Fatal("ArriveAsAgent should not be called after Version times out")
					return nil, nil
				},
			},
		},
		{
			name: "arrive",
			client: handshakeManagerClient{
				version: func(context.Context) (*rpc.VersionInfo2, error) {
					return &rpc.VersionInfo2{Version: "v2.29.2"}, nil
				},
				arrive: func(ctx context.Context) (*rpc.SessionInfo, error) {
					<-ctx.Done()
					return nil, ctx.Err()
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			oldClientFactory := NewExtendedManagerClient
			NewExtendedManagerClient = func(*grpc.ClientConn, rpc.ManagerClient) rpc.ManagerClient { return tt.client }
			t.Cleanup(func() { NewExtendedManagerClient = oldClientFactory })

			start := time.Now()
			err := TalkToManager(context.Background(), "127.0.0.1:1", &rpc.AgentInfo{}, nil)
			require.ErrorIs(t, err, context.DeadlineExceeded)
			require.Less(t, time.Since(start), time.Second)
		})
	}
}

func TestRecoverAgentSessionID(t *testing.T) {
	t.Run("fills empty ID from pod UID", func(t *testing.T) {
		session := &rpc.SessionInfo{ManagerInstallId: "manager-install"}
		recoverAgentSessionID(&rpc.AgentInfo{PodUid: "pod-uid"}, session)

		require.Equal(t, "agent:pod-uid", session.SessionId)
		require.Equal(t, "manager-install", session.ManagerInstallId)
	})

	t.Run("keeps manager-provided ID", func(t *testing.T) {
		session := &rpc.SessionInfo{SessionId: "manager-session"}
		recoverAgentSessionID(&rpc.AgentInfo{PodUid: "pod-uid"}, session)

		require.Equal(t, "manager-session", session.SessionId)
	})
}

type blockingStream[T any] struct {
	grpc.ClientStream
	ctx  context.Context
	fail <-chan struct{}
}

func (s *blockingStream[T]) Recv() (*T, error) {
	select {
	case <-s.ctx.Done():
		return nil, status.FromContextError(s.ctx.Err()).Err()
	case <-s.fail:
		return nil, status.Error(codes.Unavailable, "manager restarted")
	}
}

func (s *blockingStream[T]) CloseSend() error { return nil }

// restartableManager is an in-process manager whose CA can be swapped, and whose
// current intercept stream can be broken, to simulate a manager restart.
type restartableManager struct {
	rpc.ManagerClient
	mu              sync.Mutex
	ca              *quictunnel.CA
	failStream      chan struct{}
	reconnectCalled int
}

func (m *restartableManager) Version(context.Context, *emptypb.Empty, ...grpc.CallOption) (*rpc.VersionInfo2, error) {
	return &rpc.VersionInfo2{Version: "v2.33.0"}, nil
}

func (m *restartableManager) ArriveAsAgent(context.Context, *rpc.AgentInfo, ...grpc.CallOption) (*rpc.SessionInfo, error) {
	return &rpc.SessionInfo{SessionId: "session-a"}, nil
}

func (m *restartableManager) GetQuicAgentCert(_ context.Context, _ *rpc.SessionInfo, _ ...grpc.CallOption) (*rpc.QuicAgentCert, error) {
	m.mu.Lock()
	ca := m.ca
	m.mu.Unlock()
	sni := quicfwd.AgentSNI("test-pod-uid")
	cert, err := ca.MintServerCert(sni)
	if err != nil {
		return nil, err
	}
	certPEM, keyPEM, err := quictunnel.ServerCertToPEM(cert)
	if err != nil {
		return nil, err
	}
	return &rpc.QuicAgentCert{
		Enabled:            true,
		CertPem:            certPEM,
		KeyPem:             keyPEM,
		CaPem:              ca.CertPEM(),
		Sni:                sni,
		AuthenticationMode: "enforcing",
	}, nil
}

func (m *restartableManager) ReconnectAgent(context.Context, *rpc.ReconnectAgentRequest, ...grpc.CallOption) (*emptypb.Empty, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.reconnectCalled++
	return &emptypb.Empty{}, nil
}

func (m *restartableManager) Remain(context.Context, *rpc.RemainRequest, ...grpc.CallOption) (*emptypb.Empty, error) {
	return &emptypb.Empty{}, nil
}

func (m *restartableManager) Depart(context.Context, *rpc.SessionInfo, ...grpc.CallOption) (*emptypb.Empty, error) {
	return &emptypb.Empty{}, nil
}

func (m *restartableManager) ReviewIntercept(context.Context, *rpc.ReviewInterceptRequest, ...grpc.CallOption) (*emptypb.Empty, error) {
	return &emptypb.Empty{}, nil
}

func (m *restartableManager) WatchLogLevel(ctx context.Context, _ *emptypb.Empty, _ ...grpc.CallOption) (grpc.ServerStreamingClient[rpc.LogLevelRequest], error) {
	return &blockingStream[rpc.LogLevelRequest]{ctx: ctx}, nil
}

func (m *restartableManager) WatchInterceptsDelta(ctx context.Context, _ *rpc.SessionInfo, _ ...grpc.CallOption) (grpc.ServerStreamingClient[rpc.InterceptInfoDelta], error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return &blockingStream[rpc.InterceptInfoDelta]{ctx: ctx, fail: m.failStream}, nil
}

// restart switches to a new CA and breaks the current intercept stream.
func (m *restartableManager) restart(ca *quictunnel.CA) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ca = ca
	old := m.failStream
	m.failStream = make(chan struct{})
	close(old)
}

func (m *restartableManager) reconnects() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.reconnectCalled
}

func TestTalkToManager_RefreshesQuicCertAfterManagerRestart(t *testing.T) {
	ctx := dos.WithEnv(testutil.NewContext(t, false), dos.MapEnv{})
	ctx = dos.WithFS(ctx, aferofs.Wrap(afero.NewMemMapFs()))
	st := newFileShareAuthTestState(t, ctx)
	st.AgentConfig().WatchRetryInterval = 10 * time.Millisecond

	ca1, err := quictunnel.NewCA()
	require.NoError(t, err)
	ca2, err := quictunnel.NewCA()
	require.NoError(t, err)
	mgr := &restartableManager{ca: ca1, failStream: make(chan struct{})}

	oldClientFactory := NewExtendedManagerClient
	NewExtendedManagerClient = func(*grpc.ClientConn, rpc.ManagerClient) rpc.ManagerClient { return mgr }
	t.Cleanup(func() { NewExtendedManagerClient = oldClientFactory })

	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() {
		done <- TalkToManager(runCtx, "127.0.0.1:1", &rpc.AgentInfo{PodUid: "test-pod-uid"}, st)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			require.NoError(t, err)
		case <-time.After(5 * time.Second):
			t.Error("TalkToManager did not return after cancel")
		}
	})

	verifies := func(ca *quictunnel.CA) bool {
		token, _, err := ca.MintSessionToken("session-a")
		if err != nil {
			return false
		}
		tokenCtx := metadata.NewIncomingContext(ctx, metadata.Pairs(sessiontoken.MetadataKey, token))
		verified, err := st.fileShareAuth.verifySession(tokenCtx, tunnel.SessionID("session-a"))
		return err == nil && verified
	}
	require.Eventually(t, func() bool { return verifies(ca1) }, 5*time.Second, 10*time.Millisecond)

	mgr.restart(ca2)

	require.Eventually(t, func() bool { return verifies(ca2) }, 5*time.Second, 10*time.Millisecond,
		"agent must fetch the new manager's CA after ReconnectAgent")
	require.GreaterOrEqual(t, mgr.reconnects(), 1)
}
