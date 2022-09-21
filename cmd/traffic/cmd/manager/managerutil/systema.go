package managerutil

import (
	"context"

	"google.golang.org/grpc"

	"github.com/telepresenceio/telepresence/rpc/v2/common"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/rpc/v2/systema"
	systemarpc "github.com/telepresenceio/telepresence/rpc/v2/systema"
	"github.com/telepresenceio/telepresence/v2/pkg/a8rcloud"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
)

//go:generate mockgen -destination=mocks/systemaCRUDClient_mock.go -package=mockmanagerutil . SystemaCRUDClient
type SystemaCRUDClient interface {
	systemarpc.SystemACRUDClient
	a8rcloud.Closeable
}

type UnauthdConnProvider struct {
	Config *manager.AmbassadorCloudConfig
}

func (p *UnauthdConnProvider) GetCloudConfig(ctx context.Context) (*manager.AmbassadorCloudConfig, error) {
	return p.Config, nil
}

func (p *UnauthdConnProvider) GetAPIKey(ctx context.Context) (string, error) {
	return "", nil
}

func (p *UnauthdConnProvider) GetInstallID(ctx context.Context) (string, error) {
	return "", nil
}

func (p *UnauthdConnProvider) GetExtraHeaders(ctx context.Context) (map[string]string, error) {
	return map[string]string{}, nil
}

type unauthdClient struct {
	systemarpc.SystemACRUDClient
}

func (c *unauthdClient) Close(ctx context.Context) error {
	return nil
}
func (p *UnauthdConnProvider) BuildClient(ctx context.Context, conn *grpc.ClientConn) (SystemaCRUDClient, error) {
	client := systema.NewSystemACRUDClient(conn)
	return &unauthdClient{client}, nil
}

func AgentImageFromSystemA(ctx context.Context) (string, error) {
	// This is currently the only use case for the unauthenticated pool, but it's very important that we be able to get the image name
	systemaPool := a8rcloud.GetSystemAPool[SystemaCRUDClient](ctx, a8rcloud.UnauthdTrafficManagerConnName)
	systemaClient, err := systemaPool.Get(ctx)
	if err != nil {
		return "", err
	}
	resp, err := systemaClient.PreferredAgent(ctx, &common.VersionInfo{
		ApiVersion: client.APIVersion,
		Version:    client.Version(),
	})
	if err != nil {
		return "", err
	}
	if err = systemaPool.Done(ctx); err != nil {
		return "", err
	}
	return resp.GetImageName(), nil
}
