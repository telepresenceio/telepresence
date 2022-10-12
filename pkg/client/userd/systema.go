package userd

import (
	"context"

	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/rpc/v2/systema"
	"github.com/telepresenceio/telepresence/rpc/v2/userdaemon"
	"github.com/telepresenceio/telepresence/v2/pkg/a8rcloud"
	"github.com/telepresenceio/telepresence/v2/pkg/client/userd/auth"
	"github.com/telepresenceio/telepresence/v2/pkg/client/userd/trafficmgr"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/emptypb"
)

type SessionClientProvider struct {
	session trafficmgr.Session
}

type sessionClient struct {
	userdaemon.SystemAClient
	systema.SystemACRUDClient
	systema.UserDaemonSystemAProxyClient
}

func (c *sessionClient) Close(ctx context.Context) error {
	return nil
}

func (p *SessionClientProvider) GetCloudConfig(ctx context.Context) (*manager.AmbassadorCloudConfig, error) {
	managerClient := p.session.ManagerClient()
	cloudConfig, err := managerClient.GetCloudConfig(ctx, &emptypb.Empty{})
	if err != nil {
		return nil, err
	}
	return cloudConfig, nil
}

func (p *SessionClientProvider) GetAPIKey(ctx context.Context) (string, error) {
	return auth.GetCloudAPIKey(ctx, p.session.LoginExecutor(), a8rcloud.KeyDescWorkstation, true)
}

func (p *SessionClientProvider) GetInstallID(ctx context.Context) (string, error) {
	return "", nil
}

func (p *SessionClientProvider) GetExtraHeaders(ctx context.Context) (map[string]string, error) {
	return map[string]string{}, nil
}

func (p *SessionClientProvider) BuildClient(ctx context.Context, conn *grpc.ClientConn) (a8rcloud.SessionClient, error) {
	return &sessionClient{
		SystemAClient:                userdaemon.NewSystemAClient(conn),
		SystemACRUDClient:            systema.NewSystemACRUDClient(conn),
		UserDaemonSystemAProxyClient: systema.NewUserDaemonSystemAProxyClient(conn),
	}, nil
}
