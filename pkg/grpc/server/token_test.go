package server_test

import (
	"context"
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	"github.com/telepresenceio/telepresence/v2/pkg/grpc/server"
)

const (
	checkMethod = "/grpc.health.v1.Health/Check"
	watchMethod = "/grpc.health.v1.Health/Watch"
)

func newHealthClient(t *testing.T, token string, exempt ...string) healthpb.HealthClient {
	t.Helper()
	ctx := t.Context()
	lis := bufconn.Listen(1 << 16)
	svc := server.NewWithAuth(ctx, server.TokenInterceptors(token, exempt...))
	healthpb.RegisterHealthServer(svc, health.NewServer())
	go func() { _ = svc.Serve(lis) }()
	t.Cleanup(svc.Stop)

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return lis.Dial() }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	return healthpb.NewHealthClient(conn)
}

func TestTokenInterceptors(t *testing.T) {
	const token = "right-token"
	tests := []struct {
		name   string
		header []string
		exempt []string
		unary  codes.Code
		stream codes.Code
	}{
		{name: "no token", unary: codes.Unauthenticated, stream: codes.Unauthenticated},
		{name: "wrong token", header: []string{"wrong-token"}, unary: codes.Unauthenticated, stream: codes.Unauthenticated},
		{name: "empty token", header: []string{""}, unary: codes.Unauthenticated, stream: codes.Unauthenticated},
		{name: "right token", header: []string{token}, unary: codes.OK, stream: codes.OK},
		{name: "wrong then right token", header: []string{"wrong-token", token}, unary: codes.OK, stream: codes.OK},
		{name: "exempt unary", exempt: []string{checkMethod}, unary: codes.OK, stream: codes.Unauthenticated},
		{name: "exempt stream", exempt: []string{watchMethod}, unary: codes.Unauthenticated, stream: codes.OK},
		{name: "exempt unary with wrong token", header: []string{"wrong-token"}, exempt: []string{checkMethod}, unary: codes.OK, stream: codes.Unauthenticated},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hc := newHealthClient(t, token, tt.exempt...)
			ctx := t.Context()
			if len(tt.header) > 0 {
				ctx = metadata.AppendToOutgoingContext(ctx, appendKV(tt.header)...)
			}

			_, err := hc.Check(ctx, &healthpb.HealthCheckRequest{})
			assert.Equal(t, tt.unary, status.Code(err), "unary: %v", err)

			sctx, cancel := context.WithCancel(ctx)
			defer cancel()
			ws, err := hc.Watch(sctx, &healthpb.HealthCheckRequest{})
			require.NoError(t, err)
			_, err = ws.Recv()
			assert.Equal(t, tt.stream, status.Code(err), "stream: %v", err)
		})
	}
}

func TestTokenInterceptors_EmptyServerToken(t *testing.T) {
	hc := newHealthClient(t, "")
	ctx := metadata.AppendToOutgoingContext(t.Context(), server.TokenMetadataKey, "")
	_, err := hc.Check(ctx, &healthpb.HealthCheckRequest{})
	assert.Equal(t, codes.Unauthenticated, status.Code(err))
	_, err = hc.Check(t.Context(), &healthpb.HealthCheckRequest{})
	assert.Equal(t, codes.Unauthenticated, status.Code(err))
}

func TestNewToken(t *testing.T) {
	a, err := server.NewToken()
	require.NoError(t, err)
	b, err := server.NewToken()
	require.NoError(t, err)
	assert.Len(t, a, 64)
	assert.NotEqual(t, a, b)
}

func appendKV(values []string) []string {
	kv := make([]string, 0, 2*len(values))
	for _, v := range values {
		kv = append(kv, server.TokenMetadataKey, v)
	}
	return kv
}
