package managerutil

import (
	"context"

	"github.com/telepresenceio/telepresence/rpc/v2/common"
	systemarpc "github.com/telepresenceio/telepresence/rpc/v2/systema"
	"github.com/telepresenceio/telepresence/v2/pkg/a8rcloud"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
)

func AgentImageFromSystemA(ctx context.Context) (string, error) {
	systemaPool := a8rcloud.GetSystemAPool[interface {
		a8rcloud.Closeable
		systemarpc.SystemACRUDClient
	}](ctx, a8rcloud.TrafficManagerConnName)
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
