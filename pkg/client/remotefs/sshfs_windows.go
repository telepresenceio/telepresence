package remotefs

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/telepresenceio/telepresence/v2/pkg/client"
)

// SshfsExecutable returns the sshfs-win launcher: the configured
// intercept.sshfsPath, else the one on PATH, else the one in SSHFS-Win's
// installation directory, since its installer does not touch PATH.
func SshfsExecutable(ctx context.Context) string {
	if p := client.GetConfig(ctx).Intercept().SshfsPath; p != "" {
		return p
	}
	const exe = "sshfs-win"
	if _, err := exec.LookPath(exe); err == nil {
		return exe
	}
	for _, env := range []string{"ProgramFiles", "ProgramFiles(x86)"} {
		if pf := os.Getenv(env); pf != "" {
			p := filepath.Join(pf, "SSHFS-Win", "bin", exe+".exe")
			if _, err := os.Stat(p); err == nil {
				return p
			}
		}
	}
	return exe
}

// sshfsCommandArgs prefixes the sshfs arguments with the sshfs-win launcher's
// subcommand and the uid/gid mapping WinFsp needs.
func sshfsCommandArgs(args ...string) []string {
	return append([]string{"cmd", "-ouid=-1", "-ogid=-1"}, args...)
}

// SshfsVersionArgs returns the arguments that print the sshfs version.
func SshfsVersionArgs() []string {
	return []string{"cmd", "-V"}
}
