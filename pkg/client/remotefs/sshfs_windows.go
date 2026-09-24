package remotefs

import (
	"os"
	"os/exec"
	"path/filepath"
)

// SshfsExecutable returns the sshfs-win launcher: the one on PATH when
// present, otherwise the one in SSHFS-Win's installation directory. The
// SSHFS-Win installer does not put its bin directory on PATH.
func SshfsExecutable() string {
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
