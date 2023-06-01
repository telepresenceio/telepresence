package docker

import (
	"context"

	"github.com/docker/docker/api/types/container"
)

func StopContainer(ctx context.Context, nameOrID string) error {
	return GetClient(ctx).ContainerStop(ctx, nameOrID, container.StopOptions{})
}
