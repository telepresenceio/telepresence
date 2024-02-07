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
	APIVersion = 3
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

func IsDaemon() bool {
	const fg = "-foreground"
	a := os.Args
	return len(a) > 1 && strings.HasSuffix(a[1], fg) || len(a) > 2 && strings.HasSuffix(a[2], fg) && a[1] == "help"
}

func ProcessName() string {
	const fg = "-foreground"
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
	return strings.TrimSuffix(pn, fg)
}
