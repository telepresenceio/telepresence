package trafficmgr

import (
	"context"
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/blang/semver/v4"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/telepresenceio/clog/testutil"
	"github.com/telepresenceio/telepresence/rpc/v2/connector"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/k8s"
	"github.com/telepresenceio/telepresence/v2/pkg/client/userd"
)

type remainTestManager struct {
	manager.UnimplementedManagerServer
	mu   sync.Mutex
	errs []error

	// reconnectErr is returned by ReconnectClient.
	reconnectErr error
}

func (s *remainTestManager) Remain(context.Context, *manager.RemainRequest) (*emptypb.Empty, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.errs) == 0 {
		return &emptypb.Empty{}, nil
	}
	err := s.errs[0]
	s.errs = s.errs[1:]
	if err != nil {
		return nil, err
	}
	return &emptypb.Empty{}, nil
}

func (s *remainTestManager) ReconnectClient(context.Context, *manager.ReconnectClientRequest) (*emptypb.Empty, error) {
	if s.reconnectErr != nil {
		return nil, s.reconnectErr
	}
	return &emptypb.Empty{}, nil
}

type disconnectCounter struct {
	connector.UnimplementedConnectorServer
	n atomic.Int32
}

func (d *disconnectCounter) Disconnect(context.Context, *emptypb.Empty) (*emptypb.Empty, error) {
	d.n.Add(1)
	return &emptypb.Empty{}, nil
}

type disconnectService struct {
	userd.Service
	srv connector.ConnectorServer
}

func (d disconnectService) ConnectorServer() connector.ConnectorServer { return d.srv }

// TestReconnectFailureEndsSession covers the decision that a regression test cannot reach
// reliably: which ReconnectClient errors are final, and that several watchers hitting the
// same failure disconnect once. The cluster path for FailedPrecondition is covered by the
// regression suite, but a cluster cannot be made to answer Unavailable or NotFound on demand.
func TestReconnectFailureEndsSession(t *testing.T) {
	tests := []struct {
		name  string
		err   error
		final bool
	}{
		{"namespace not managed", status.Error(codes.FailedPrecondition, "namespace x is not managed"), true},
		{"not found", status.Error(codes.NotFound, "gone"), true},
		{"unavailable", status.Error(codes.Unavailable, "restarting"), false},
		{"deadline", status.Error(codes.DeadlineExceeded, "slow"), false},
		{"unauthenticated", status.Error(codes.Unauthenticated, "token expired"), false},
		{"permission denied", status.Error(codes.PermissionDenied, "no"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newRemainTestSession(t)
			dc := &disconnectCounter{}
			s.service = disconnectService{srv: dc}
			failedGeneration := s.currentManagerGeneration()
			connect := func(context.Context) (*grpc.ClientConn, string, semver.Version, error) {
				// A failed reconnect closes the connection, so each attempt needs its own.
				conn, cleanup := dialRemainTestManager(t, &remainTestManager{reconnectErr: tt.err})
				t.Cleanup(cleanup)
				return conn, "manager", semver.Version{Major: 2, Minor: 32}, nil
			}
			for range 3 {
				require.Error(t, s.reconnectManagerWith(failedGeneration, connect))
			}
			if tt.final {
				require.Eventually(t, func() bool { return dc.n.Load() == 1 }, 5*time.Second, 10*time.Millisecond)
				time.Sleep(50 * time.Millisecond)
			} else {
				time.Sleep(100 * time.Millisecond)
			}
			want := int32(0)
			if tt.final {
				want = 1
			}
			require.Equal(t, want, dc.n.Load())
		})
	}
}

func TestRemainReconnectsExpiredSession(t *testing.T) {
	s := newRemainTestSession(t, status.Error(codes.NotFound, "session expired"))

	reconnects := 0
	err := s.remainWith(func(uint64) error {
		reconnects++
		return nil
	})

	require.NoError(t, err)
	require.Equal(t, 1, reconnects)
}

func TestRemainReconnectsAfterConsecutiveRetryableFailures(t *testing.T) {
	s := newRemainTestSession(t,
		status.Error(codes.DeadlineExceeded, "first timeout"),
		status.Error(codes.Unavailable, "manager unavailable"),
		status.Error(codes.DeadlineExceeded, "third timeout"),
	)

	reconnects := 0
	reconnect := func(uint64) error {
		reconnects++
		return nil
	}

	for range remainReconnectFailureThreshold - 1 {
		require.NoError(t, s.remainWith(reconnect))
		require.Zero(t, reconnects)
	}
	require.NoError(t, s.remainWith(reconnect))
	require.Equal(t, 1, reconnects)
}

func TestRemainSuccessResetsRetryableFailureCount(t *testing.T) {
	s := newRemainTestSession(t,
		status.Error(codes.Unavailable, "first failure"),
		nil,
		status.Error(codes.Unavailable, "second failure"),
		status.Error(codes.Unavailable, "third failure"),
		status.Error(codes.Unavailable, "fourth failure"),
	)

	reconnects := 0
	reconnect := func(uint64) error {
		reconnects++
		return nil
	}

	for range 4 {
		require.NoError(t, s.remainWith(reconnect))
		require.Zero(t, reconnects)
	}
	require.NoError(t, s.remainWith(reconnect))
	require.Equal(t, 1, reconnects)
}

func TestRemainRetriesAfterReconnectFailure(t *testing.T) {
	s := newRemainTestSession(t,
		status.Error(codes.NotFound, "session expired"),
		status.Error(codes.NotFound, "session expired"),
	)
	expectedErr := errors.New("reconnect failed")

	reconnects := 0
	reconnect := func(uint64) error {
		reconnects++
		if reconnects == 1 {
			return expectedErr
		}
		return nil
	}

	require.NoError(t, s.remainWith(reconnect))
	require.NoError(t, s.remainWith(reconnect))
	require.Equal(t, 2, reconnects)
}

func TestRemainFailureCountResetsForNewManagerGeneration(t *testing.T) {
	s := newRemainTestSession(t,
		status.Error(codes.Unavailable, "first failure"),
		status.Error(codes.Unavailable, "second failure"),
		status.Error(codes.Unavailable, "failure after replacement"),
	)

	reconnects := 0
	reconnect := func(uint64) error {
		reconnects++
		return nil
	}

	require.NoError(t, s.remainWith(reconnect))
	require.NoError(t, s.remainWith(reconnect))
	require.Zero(t, reconnects)

	s.managerLock.Lock()
	s.managerGeneration++
	s.managerLock.Unlock()

	require.NoError(t, s.remainWith(reconnect))
	require.Zero(t, reconnects)
}

func TestReconnectManagerCoalescesSameGeneration(t *testing.T) {
	s := newRemainTestSession(t)
	failedGeneration := s.currentManagerGeneration()
	newConn, cleanup := dialRemainTestManager(t, &remainTestManager{})
	t.Cleanup(cleanup)

	firstConnectStarted := make(chan struct{})
	releaseFirstConnect := make(chan struct{})
	var connects atomic.Int32
	connect := func(context.Context) (*grpc.ClientConn, string, semver.Version, error) {
		if connects.Add(1) == 1 {
			close(firstConnectStarted)
			<-releaseFirstConnect
		}
		return newConn, "manager", semver.Version{Major: 2, Minor: 32}, nil
	}

	errs := make(chan error, 2)
	go func() {
		errs <- s.reconnectManagerWith(failedGeneration, connect)
	}()
	<-firstConnectStarted
	go func() {
		errs <- s.reconnectManagerWith(failedGeneration, connect)
	}()
	close(releaseFirstConnect)

	require.NoError(t, <-errs)
	require.NoError(t, <-errs)
	require.EqualValues(t, 1, connects.Load())
	_, name, version, generation := s.managerSnapshot()
	require.Equal(t, "manager", name)
	require.Equal(t, semver.Version{Major: 2, Minor: 32}, version)
	require.Equal(t, failedGeneration+1, generation)
}

func TestReconnectManagerRetriesAfterFailedRepair(t *testing.T) {
	s := newRemainTestSession(t)
	failedGeneration := s.currentManagerGeneration()
	newConn, cleanup := dialRemainTestManager(t, &remainTestManager{})
	t.Cleanup(cleanup)

	var connects atomic.Int32
	expectedErr := errors.New("connect failed")
	connect := func(context.Context) (*grpc.ClientConn, string, semver.Version, error) {
		if connects.Add(1) == 1 {
			return nil, "", semver.Version{}, expectedErr
		}
		return newConn, "manager", semver.Version{Major: 2, Minor: 32}, nil
	}

	require.ErrorIs(t, s.reconnectManagerWith(failedGeneration, connect), expectedErr)
	require.NoError(t, s.reconnectManagerWith(failedGeneration, connect))
	require.EqualValues(t, 2, connects.Load())
	generation := s.currentManagerGeneration()
	require.Equal(t, failedGeneration+1, generation)
}

func newRemainTestSession(t *testing.T, errs ...error) *session {
	t.Helper()
	conn, cleanup := dialRemainTestManager(t, &remainTestManager{errs: errs})
	t.Cleanup(cleanup)

	ctx := client.WithConfig(testutil.NewContext(t, false), client.GetDefaultConfig())
	return &session{
		Cluster:     &k8s.Cluster{Kubeconfig: &k8s.Kubeconfig{Context: ctx}},
		managerConn: conn,
		sessionInfo: &manager.SessionInfo{SessionId: "test-session"},
	}
}

func dialRemainTestManager(t *testing.T, server manager.ManagerServer) (*grpc.ClientConn, func()) {
	t.Helper()
	listener := bufconn.Listen(1024 * 1024)
	grpcServer := grpc.NewServer()
	manager.RegisterManagerServer(grpcServer, server)
	go func() {
		_ = grpcServer.Serve(listener)
	}()
	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) {
			return listener.Dial()
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	return conn, func() {
		conn.Close()
		grpcServer.Stop()
		listener.Close()
	}
}
