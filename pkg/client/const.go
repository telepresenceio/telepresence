package client

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/telepresenceio/telepresence/v2/pkg/dos"
)

const (
	// APIVersion is the API version of the daemon and connector API.
	APIVersion         = 3
	UserDaemonName     = "userd"
	RootDaemonName     = "rootd"
	KubeAuthDaemonName = "kubeauthd"
)

// DisplayVersion returns a printable version for `telepresence`.
func DisplayVersion() string {
	return fmt.Sprintf("%s (api v%d)", Version(), APIVersion)
}

// GetExe returns the name of the running executable.
func GetExe(ctx context.Context) string {
	// Figure out our executable
	exeName, err := dos.Executable(ctx)
	if err != nil {
		panic(err)
	}
	return exeName
}

func isDaemonName(name string) bool {
	switch name {
	case UserDaemonName, RootDaemonName, KubeAuthDaemonName:
		return true
	default:
		return false
	}
}

func IsDaemon() bool {
	return isDaemonName(ProcessName())
}

var ProcessName = func() string { //nolint:gochecknoglobals // extension point
	a := os.Args
	var pn string
	switch {
	case len(a) > 2 && a[1] == "help":
		pn = a[2]
	case len(a) > 1:
		pn = a[1]
	default:
		pn = filepath.Base(a[0])
		if runtime.GOOS == "windows" {
			pn = strings.TrimSuffix(pn, ".exe")
		}
	}
	return pn
}
