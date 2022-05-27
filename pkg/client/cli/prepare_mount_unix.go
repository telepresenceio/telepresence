//go:build !windows
// +build !windows

package cli

import (
	"os"
	"path/filepath"
)

func prepareMount(mountPoint string) (string, error) {
	if mountPoint == "" {
		return os.MkdirTemp("", "telfs-")
	} else {
		var err error
		mountPoint, err = filepath.Abs(mountPoint)
		if err != nil {
			return "", err
		}
	}
	return mountPoint, os.MkdirAll(mountPoint, 0700)
}
