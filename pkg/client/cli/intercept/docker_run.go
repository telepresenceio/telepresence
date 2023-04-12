package intercept

import (
	"context"
	"fmt"

	"github.com/datawire/dlib/dexec"
	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/flags"
	"github.com/telepresenceio/telepresence/v2/pkg/proc"
	"github.com/telepresenceio/telepresence/v2/pkg/shellquote"
)

func (s *state) startInDocker(ctx context.Context, envFile string, args []string) (*dexec.Cmd, error) {
	ourArgs := []string{
		"run",
		"--env-file", envFile,
		"--dns-search", "tel2-search",
	}

	name, err := flags.GetUnparsedValue(args, "--name")
	if err != nil {
		return nil, err
	}
	if name == "" {
		name = fmt.Sprintf("intercept-%s-%d", s.Name(), s.localPort)
		ourArgs = append(ourArgs, "--name", name)
	}

	if s.dockerPort != 0 {
		ourArgs = append(ourArgs, "-p", fmt.Sprintf("%d:%d", s.localPort, s.dockerPort))
	}

	dockerMount := ""
	if s.mountPoint != "" { // do we have a mount point at all?
		if dockerMount = s.DockerMount; dockerMount == "" {
			dockerMount = s.mountPoint
		}
	}
	if dockerMount != "" {
		ourArgs = append(ourArgs, "-v", fmt.Sprintf("%s:%s", s.mountPoint, dockerMount))
	}
	args = append(ourArgs, args...)
	cmd := proc.CommandContext(ctx, "docker", args...)
	cmd.DisableLogging = true
	cmd.Stdout = s.cmd.OutOrStdout()
	cmd.Stderr = s.cmd.ErrOrStderr()
	cmd.Stdin = s.cmd.InOrStdin()
	dlog.Debugf(ctx, shellquote.ShellString("docker", args))
	if err = cmd.Start(); err != nil {
		return nil, err
	}
	return cmd, err
}
