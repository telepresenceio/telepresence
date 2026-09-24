//go:build !windows

package remotefs

// SshfsExecutable returns the sshfs executable name.
func SshfsExecutable() string {
	return "sshfs"
}

func sshfsCommandArgs(args ...string) []string {
	return args
}

// SshfsVersionArgs returns the arguments that print the sshfs version.
func SshfsVersionArgs() []string {
	return []string{"-V"}
}
