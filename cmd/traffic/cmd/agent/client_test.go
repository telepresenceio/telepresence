package agent

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/emptypb"

	rpc "github.com/telepresenceio/telepresence/rpc/v2/manager"
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
