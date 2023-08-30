package integration_test

import (
	"bufio"
	"os"
	"path/filepath"
	"regexp"

	"github.com/telepresenceio/telepresence/v2/integration_test/itest"
	"github.com/telepresenceio/telepresence/v2/pkg/filelocation"
)

func (s *notConnectedSuite) Test_RootDaemonLogLevel() {
	require := s.Require()
	ctx := s.Context()
	s.TelepresenceConnect(ctx)
	itest.TelepresenceQuitOk(ctx)
	rootLogName := filepath.Join(filelocation.AppUserLogDir(ctx), "daemon.log")
	rootLog, err := os.Open(rootLogName)
	require.NoError(err)
	defer rootLog.Close()

	hasDebug := false
	scn := bufio.NewScanner(rootLog)
	match := regexp.MustCompile(` debug +daemon/server`)
	for scn.Scan() && !hasDebug {
		hasDebug = match.MatchString(scn.Text())
	}
	s.True(hasDebug, "daemon.log does not contain expected debug statements")
}
