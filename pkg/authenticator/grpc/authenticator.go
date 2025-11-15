package grpc

import (
	"context"
	"fmt"

	"google.golang.org/grpc"

	"github.com/telepresenceio/dlib/v2/dlog"
	rpc "github.com/telepresenceio/telepresence/rpc/v2/authenticator"
	"github.com/telepresenceio/telepresence/v2/pkg/authenticator"
	"github.com/telepresenceio/telepresence/v2/pkg/k8sapi"
)

func RegisterAuthenticatorServer(srv *grpc.Server, clientConfigProvider k8sapi.ClientConfigProvider) {
	rpc.RegisterAuthenticatorServer(srv, &AuthenticatorServer{
		authenticator: authenticator.NewService(clientConfigProvider),
	})
}

type Authenticator interface {
	GetExecCredentials(ctx context.Context, contextName string) ([]byte, error)
}

type AuthenticatorServer struct {
	rpc.UnsafeAuthenticatorServer

	authenticator Authenticator
}

// GetContextExecCredentials returns credentials for a particular Kubernetes context on the host machine.
func (h *AuthenticatorServer) GetContextExecCredentials(ctx context.Context, request *rpc.GetContextExecCredentialsRequest) (*rpc.GetContextExecCredentialsResponse, error) {
	dlog.Debugf(ctx, "GetContextExecCredentials(%s)", request.ContextName)
	rawExecCredentials, err := h.authenticator.GetExecCredentials(ctx, request.ContextName)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve exec credentils: %w", err)
	}

	return &rpc.GetContextExecCredentialsResponse{
		RawCredentials: rawExecCredentials,
	}, nil
}
