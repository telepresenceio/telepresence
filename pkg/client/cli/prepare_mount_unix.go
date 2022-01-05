//go:build !windows
// +build !windows

package cli

import (
	"os"
)

func prepareMount(mountPoint string) (string, error) {
	if mountPoint == "" {
		return os.MkdirTemp("", "telfs-")
	}
	return mountPoint, os.MkdirAll(mountPoint, 0700)
}
