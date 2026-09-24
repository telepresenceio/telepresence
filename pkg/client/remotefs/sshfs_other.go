//go:build !windows

package remotefs

import (
	"context"

	"github.com/telepresenceio/telepresence/v2/pkg/client"
)

// SshfsExecutable returns the configured intercept.sshfsPath, else "sshfs".
func SshfsExecutable(ctx context.Context) string {
	if p := client.GetConfig(ctx).Intercept().SshfsPath; p != "" {
		return p
	}
	return "sshfs"
}

func sshfsCommandArgs(args ...string) []string {
	return args
}

// SshfsVersionArgs returns the arguments that print the sshfs version.
func SshfsVersionArgs() []string {
	return []string{"-V"}
}
