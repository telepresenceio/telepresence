package kubeauth

import (
	"context"
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	rpc "github.com/telepresenceio/telepresence/rpc/v2/authenticator"
	"github.com/telepresenceio/telepresence/v2/pkg/grpc/server"
)

type fakeAuthenticator struct {
	rpc.UnimplementedAuthenticatorServer
}

func (fakeAuthenticator) GetContextExecCredentials(context.Context, *rpc.GetContextExecCredentialsRequest) (*rpc.GetContextExecCredentialsResponse, error) {
	return &rpc.GetContextExecCredentialsResponse{RawCredentials: []byte("creds")}, nil
}

func TestTokenInterceptors(t *testing.T) {
	const token = "right-token"
	ctx := t.Context()
	lis := bufconn.Listen(1 << 16)
	svc := server.NewWithAuth(ctx, tokenInterceptors(token))
	rpc.RegisterAuthenticatorServer(svc, fakeAuthenticator{})
	go func() { _ = svc.Serve(lis) }()
	t.Cleanup(svc.Stop)

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return lis.Dial() }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	ac := rpc.NewAuthenticatorClient(conn)

	call := func(ctx context.Context) (*rpc.GetContextExecCredentialsResponse, error) {
		return ac.GetContextExecCredentials(ctx, &rpc.GetContextExecCredentialsRequest{ContextName: "c"})
	}

	_, err = call(ctx)
	assert.Equal(t, codes.Unauthenticated, status.Code(err))

	_, err = call(metadata.AppendToOutgoingContext(ctx, TokenMetadataKey, "wrong-token"))
	assert.Equal(t, codes.Unauthenticated, status.Code(err))

	res, err := call(metadata.AppendToOutgoingContext(ctx, TokenMetadataKey, token))
	require.NoError(t, err)
	assert.Equal(t, "creds", string(res.RawCredentials))
}
