package server_test

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"

	"github.com/telepresenceio/telepresence/v2/pkg/grpc/server"
)

func TestTrackConns(t *testing.T) {
	var mu sync.Mutex
	var seen []uint64
	closed := make(chan uint64, 4)
	auth := &server.Interceptors{
		Unary: func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
			id, ok := server.ConnID(ctx)
			require.True(t, ok)
			mu.Lock()
			seen = append(seen, id)
			mu.Unlock()
			return handler(ctx, req)
		},
	}
	svc := server.NewWithAuth(t.Context(), auth, server.TrackConns(func(id uint64) { closed <- id }))
	healthpb.RegisterHealthServer(svc, health.NewServer())
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	go func() { _ = svc.Serve(lis) }()
	t.Cleanup(svc.Stop)

	dial := func() *grpc.ClientConn {
		cc, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
		require.NoError(t, err)
		return cc
	}
	check := func(cc *grpc.ClientConn) {
		_, err := healthpb.NewHealthClient(cc).Check(t.Context(), &healthpb.HealthCheckRequest{})
		require.NoError(t, err)
	}

	c1 := dial()
	check(c1)
	check(c1)
	c2 := dial()
	check(c2)

	mu.Lock()
	require.Len(t, seen, 3)
	require.Equal(t, seen[0], seen[1])
	require.NotEqual(t, seen[0], seen[2])
	first := seen[0]
	mu.Unlock()

	require.NoError(t, c1.Close())
	select {
	case id := <-closed:
		require.Equal(t, first, id)
	case <-time.After(5 * time.Second):
		t.Fatal("close callback not called")
	}
	require.NoError(t, c2.Close())
}

func TestConnID_NotTracked(t *testing.T) {
	_, ok := server.ConnID(t.Context())
	require.False(t, ok)
}
