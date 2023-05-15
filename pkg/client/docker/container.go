package docker

import (
	"context"

	"github.com/telepresenceio/telepresence/v2/pkg/proc"
)

func StopContainer(ctx context.Context, nameOrID string) error {
	cmd := proc.CommandContext(ctx, "docker", "stop", "--time", "5", nameOrID)
	_, err := proc.CaptureErr(ctx, cmd)
	return err
}
