package userd

import (
	"context"
	"net"

	"github.com/telepresenceio/telepresence/rpc/v2/systema"
	"github.com/telepresenceio/telepresence/rpc/v2/userdaemon"
	"github.com/telepresenceio/telepresence/v2/pkg/a8rcloud"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/cliutil"
	"github.com/telepresenceio/telepresence/v2/pkg/client/userd/trafficmgr"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/emptypb"
)

type SessionClientProvider struct {
	session trafficmgr.Session
}

type SessionClient struct {
	userdaemon.SystemAClient
	systema.UserDaemonSystemAProxyClient
}

func (c *SessionClient) Close(ctx context.Context) error {
	return nil
}

func (p *SessionClientProvider) GetSystemaAddress(ctx context.Context) (string, error) {
	managerClient := p.session.ManagerClient()
	cloudConfig, err := managerClient.GetCloudConfig(ctx, &emptypb.Empty{})
	if err != nil {
		return "", err
	}
	return net.JoinHostPort(cloudConfig.GetHost(), cloudConfig.GetPort()), nil
}

func (p *SessionClientProvider) GetAPIKey(ctx context.Context) (string, error) {
	return cliutil.GetCloudAPIKey(ctx, a8rcloud.KeyDescWorkstation, true)
}

func (p *SessionClientProvider) GetInstallID(ctx context.Context) (string, error) {
	return "", nil
}

func (p *SessionClientProvider) GetExtraHeaders(ctx context.Context) (map[string]string, error) {
	return map[string]string{}, nil
}

func (p *SessionClientProvider) BuildClient(ctx context.Context, conn *grpc.ClientConn) (*SessionClient, error) {
	userdCli := userdaemon.NewSystemAClient(conn)
	userdProxyCli := systema.NewUserDaemonSystemAProxyClient(conn)
	return &SessionClient{SystemAClient: userdCli, UserDaemonSystemAProxyClient: userdProxyCli}, nil
}
