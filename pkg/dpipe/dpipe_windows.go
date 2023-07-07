package dpipe

import (
	"context"
	"os"
	"os/exec" //nolint:depguard // We want no logging and no soft-context signal handling

	"github.com/telepresenceio/telepresence/v2/pkg/proc"
)

func killProcess(ctx context.Context, cmd *exec.Cmd) {
	proc.KillProcessGroup(ctx, cmd, os.Kill)
}
