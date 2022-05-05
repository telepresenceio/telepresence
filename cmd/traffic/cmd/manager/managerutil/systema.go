package managerutil

import (
	"context"

	"github.com/telepresenceio/telepresence/rpc/v2/common"
	systemarpc "github.com/telepresenceio/telepresence/rpc/v2/systema"
	"github.com/telepresenceio/telepresence/v2/pkg/a8rcloud"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
)

type SystemaCRUDClient interface {
	systemarpc.SystemACRUDClient
	a8rcloud.Closeable
}

func AgentImageFromSystemA(ctx context.Context) (string, error) {
	systemaPool := a8rcloud.GetSystemAPool[SystemaCRUDClient](ctx, a8rcloud.TrafficManagerConnName)
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
