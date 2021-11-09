package integration_test

import (
	"bufio"
	"os"
	"path/filepath"
	"regexp"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/integration_test/itest"
	"github.com/telepresenceio/telepresence/v2/pkg/client/logging"
	"github.com/telepresenceio/telepresence/v2/pkg/filelocation"
)

func (s *notConnectedSuite) Test_RootDaemonLogLevel() {
	require := s.Require()
	ctx := s.Context()
	itest.TelepresenceOk(ctx, "connect")
	itest.TelepresenceQuitOk(ctx)
	logDir, err := filelocation.AppUserLogDir(ctx)
	require.NoError(err)
	rootLogName := filepath.Join(logDir, "daemon.log")
	rootLog, err := os.Open(rootLogName)
	require.NoError(err)
	defer func() {
		_ = rootLog.Close()
		rootLog, err = os.Open(rootLogName)
		if err != nil {
			dlog.Errorf(ctx, "open failed on %q failed: %v", rootLogName, err)
			return
		}
		stat, err := logging.FStat(rootLog)
		_ = rootLog.Close()
		if err != nil {
			dlog.Errorf(ctx, "stat on %q failed: %v", rootLogName, err)
			return
		}
		if err := os.Remove(rootLogName); err != nil {
			dlog.Errorf(ctx, "Failed to remove %q: %v", rootLogName, err)
			dlog.Error(ctx, stat)
		}
	}()

	hasDebug := false
	scn := bufio.NewScanner(rootLog)
	match := regexp.MustCompile(` debug +daemon/server`)
	for scn.Scan() && !hasDebug {
		hasDebug = match.MatchString(scn.Text())
	}
	s.True(hasDebug, "daemon.log does not contain expected debug statements")
}
