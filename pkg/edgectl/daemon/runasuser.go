package daemon

import (
	"runtime"

	"github.com/kballard/go-shellquote"

	"github.com/datawire/ambassador/pkg/api/edgectl/rpc"
	"github.com/datawire/ambassador/pkg/supervisor"
)

// RunAsInfo contains the information required to launch a subprocess as the
// user such that it is likely to function as if the user launched it
// themselves.
type RunAsInfo struct {
	Name string
	Cwd  string
	Env  []string
}

func runAsUserFromRPC(u *rpc.ConnectRequest_UserInfo) *RunAsInfo {
	return &RunAsInfo{
		Name: u.Name,
		Cwd:  u.Cwd,
		Env:  u.Env,
	}
}

// Command returns a supervisor.Cmd that is configured to run a subprocess as
// the user in this context.
func (rai *RunAsInfo) Command(p *supervisor.Process, args ...string) *supervisor.Cmd {
	if rai == nil {
		rai = &RunAsInfo{}
	}
	var cmd *supervisor.Cmd
	if rai.Name == "root" || len(rai.Name) == 0 {
		cmd = p.Command(args[0], args[1:]...)
	} else {
		if runtime.GOOS == "darwin" {
			// MacOS `su` doesn't appear to propagate signals and
			// `sudo` is always (?) available.
			sudoOpts := []string{"--user", rai.Name, "--set-home", "--preserve-env", "--"}
			cmd = p.Command("sudo", append(sudoOpts, args...)...)
		} else {
			// FIXME(ark3): The above _should_ work on Linux, but
			// doesn't work on my machine. I don't know why (yet).
			cmd = p.Command("su", "-m", rai.Name, "-s", "/bin/sh", "-c", shellquote.Join(args...))
		}
	}
	cmd.Env = rai.Env
	cmd.Dir = rai.Cwd
	return cmd
}
