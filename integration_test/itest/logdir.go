package itest

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/pkg/filelocation"
)

func CleanLogDir(ctx context.Context, require *Requirements, nsRx, mgrNamespace, svcNameRx string) {
	logDir := filelocation.AppUserLogDir(ctx)
	files, err := os.ReadDir(logDir)
	require.NoError(err)
	match := regexp.MustCompile(
		fmt.Sprintf(`^(?:traffic-manager-[0-9a-z-]+\.%s|%s-[0-9a-z-]+\.%s)\.(?:log|yaml)$`,
			mgrNamespace, svcNameRx, nsRx))

	for _, file := range files {
		if match.MatchString(file.Name()) {
			dlog.Infof(ctx, "Deleting log-file %s", file.Name())
			require.NoError(os.Remove(filepath.Join(logDir, file.Name())))
		}
	}
}
