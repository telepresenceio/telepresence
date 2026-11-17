package daemon_test

import (
	"context"
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"

	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/daemon"
	tpGrpc "github.com/telepresenceio/telepresence/v2/pkg/grpc"
	"github.com/telepresenceio/telepresence/v2/pkg/grpc/server"
)

func TestIsRootDaemonInUse_ThroughServer(t *testing.T) {
	auth := &server.Interceptors{
		Unary: func(context.Context, any, *grpc.UnaryServerInfo, grpc.UnaryHandler) (any, error) {
			return nil, daemon.ErrRootDaemonInUse()
		},
	}
	svc := server.NewWithAuth(t.Context(), auth)
	healthpb.RegisterHealthServer(svc, health.NewServer())
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	go func() { _ = svc.Serve(lis) }()
	t.Cleanup(svc.Stop)

	cc, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = cc.Close() })

	_, err = healthpb.NewHealthClient(cc).Check(t.Context(), &healthpb.HealthCheckRequest{})
	require.Error(t, err)
	assert.True(t, daemon.IsRootDaemonInUse(err), "raw: %v", err)
	assert.True(t, daemon.IsRootDaemonInUse(tpGrpc.FromGRPC(err)), "converted: %v", err)
}
