package grpc

import (
	"context"
	"fmt"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/telepresenceio/clog"
	rpc "github.com/telepresenceio/telepresence/rpc/v2/authenticator"
	"github.com/telepresenceio/telepresence/v2/pkg/authenticator"
	"github.com/telepresenceio/telepresence/v2/pkg/k8sapi"
)

// RegisterAuthenticatorServer registers a server that resolves credentials for the given context only.
func RegisterAuthenticatorServer(srv *grpc.Server, clientConfigProvider k8sapi.ClientConfigProvider, contextName string) {
	rpc.RegisterAuthenticatorServer(srv, &AuthenticatorServer{
		authenticator: authenticator.NewService(clientConfigProvider),
		contextName:   contextName,
	})
}

type Authenticator interface {
	GetExecCredentials(ctx context.Context, contextName string) ([]byte, error)
}

type AuthenticatorServer struct {
	rpc.UnsafeAuthenticatorServer

	authenticator Authenticator
	contextName   string
}

// GetContextExecCredentials returns credentials for a particular Kubernetes context on the host machine.
func (h *AuthenticatorServer) GetContextExecCredentials(ctx context.Context, request *rpc.GetContextExecCredentialsRequest) (*rpc.GetContextExecCredentialsResponse, error) {
	clog.Debugf(ctx, "GetContextExecCredentials(%s)", request.ContextName)
	if request.ContextName != h.contextName {
		return nil, status.Errorf(codes.PermissionDenied, "context %q is not served", request.ContextName)
	}
	rawExecCredentials, err := h.authenticator.GetExecCredentials(ctx, request.ContextName)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve exec credentils: %w", err)
	}

	return &rpc.GetContextExecCredentialsResponse{
		RawCredentials: rawExecCredentials,
	}, nil
}
