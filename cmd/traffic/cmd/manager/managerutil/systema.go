package managerutil

import (
	"context"

	"github.com/telepresenceio/telepresence/rpc/v2/common"
	"github.com/telepresenceio/telepresence/rpc/v2/systema"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
)

type SystemAPool interface {
	Get() (systema.SystemACRUDClient, error)
	Done() error
}

type systemaPoolKey struct{}

func WithSystemAPool(ctx context.Context, pool SystemAPool) context.Context {
	return context.WithValue(ctx, systemaPoolKey{}, pool)
}

func GetSystemAPool(ctx context.Context) SystemAPool {
	if p, ok := ctx.Value(systemaPoolKey{}).(SystemAPool); ok {
		return p
	}
	return nil
}

func AgentImageFromSystemA(ctx context.Context) (string, error) {
	systemaPool := GetSystemAPool(ctx)
	systemaClient, err := systemaPool.Get()
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
	if err = systemaPool.Done(); err != nil {
		return "", err
	}
	return resp.GetImageName(), nil
}
