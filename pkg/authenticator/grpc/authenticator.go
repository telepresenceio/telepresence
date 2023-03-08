package grpc

import (
	"context"
	"fmt"

	"google.golang.org/grpc"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/datawire/dlib/dlog"
	rpc "github.com/telepresenceio/telepresence/rpc/v2/authenticator"
	"github.com/telepresenceio/telepresence/v2/pkg/authenticator"
)

func RegisterAuthenticatorServer(srv *grpc.Server, kubeClientConfig clientcmd.ClientConfig) {
	rpc.RegisterAuthenticatorServer(srv, &AuthenticatorServer{
		authenticator: authenticator.NewService(kubeClientConfig),
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
