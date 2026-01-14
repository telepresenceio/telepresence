package proc

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"

	"github.com/telepresenceio/dlib/v2/dlog"
	"github.com/telepresenceio/telepresence/v2/pkg/dos"
	"github.com/telepresenceio/telepresence/v2/pkg/shellquote"
)

// StdCommand returns a command that redirects stdout and stderr to dos.Stdout and dos.Stderr
// and performs no logging.
func StdCommand(ctx context.Context, exe string, args ...string) *exec.Cmd {
	cmd := CommandContext(ctx, exe, args...)
	cmd.Stdout = dos.Stdout(ctx)
	cmd.Stderr = dos.Stderr(ctx)
	dlog.Debug(ctx, shellquote.ShellString(exe, args))
	return cmd
}

// CaptureErr disables command logging, captures Stdout and Stderr to two different buffers.
// If an error occurs, the stdout output is discarded and the stderr output is included in the
// returned error unless the error itself already contains that output.
// On success, any output on stderr is discarded and the stdout output is returned.
func CaptureErr(cmd *exec.Cmd) ([]byte, error) {
	var stdOut, stdErr bytes.Buffer
	cmd.Stdout = &stdOut
	cmd.Stderr = &stdErr
	if err := cmd.Run(); err != nil {
		if es := strings.TrimSpace(stdErr.String()); es != "" {
			if !strings.Contains(err.Error(), es) {
				err = fmt.Errorf("%s: %w", es, err)
			}
		}
		return nil, err
	}
	return stdOut.Bytes(), nil
}
