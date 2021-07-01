// +build !windows

package proc

import "os"

func IsAdmin() bool {
	return os.Geteuid() == 0
}
