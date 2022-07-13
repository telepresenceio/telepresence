//go:build !windows
// +build !windows

package cliutil

import (
	"os"
	"path/filepath"
)

func PrepareMount(mountPoint string) (string, error) {
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
