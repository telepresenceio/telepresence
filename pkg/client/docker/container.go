package docker

import (
	"context"

	"github.com/docker/docker/api/types/container"
)

func StopContainer(ctx context.Context, nameOrID string) error {
	cli, err := GetClient(ctx)
	if err == nil {
		err = cli.ContainerStop(ctx, nameOrID, container.StopOptions{})
	}
	return err
}
