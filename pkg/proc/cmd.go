package proc

import (
	"context"

	"github.com/datawire/dlib/dexec"
	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/pkg/dos"
	"github.com/telepresenceio/telepresence/v2/pkg/shellquote"
)

// StdCommand returns a command that redirects stdout and stderr to dos.Stdout and dos.Stderr
// and performs no logging.
func StdCommand(ctx context.Context, exe string, args ...string) *dexec.Cmd {
	cmd := CommandContext(ctx, exe, args...)
	cmd.DisableLogging = true
	cmd.Stdout = dos.Stdout(ctx)
	cmd.Stderr = dos.Stderr(ctx)
	dlog.Debugf(ctx, shellquote.ShellString(exe, args))
	return cmd
}
