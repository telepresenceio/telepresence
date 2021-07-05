// +build !windows

package cli

import (
	"io/ioutil"
	"os"
)

func prepareMount(mountPoint string) (string, error) {
	if mountPoint == "" {
		return ioutil.TempDir("", "telfs-")
	}
	return mountPoint, os.MkdirAll(mountPoint, 0700)
}
