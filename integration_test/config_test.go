package integration_test

import (
	"os"
	"path/filepath"

	"github.com/telepresenceio/telepresence/v2/integration_test/itest"
	"github.com/telepresenceio/telepresence/v2/pkg/filelocation"
)

func (s *notConnectedSuite) Test_EmptyConfigFile() {
	ctx := s.Context()
	cfgDir := itest.TempDir(ctx)
	ctx = filelocation.WithAppUserConfigDir(ctx, cfgDir)
	f, err := os.Create(filepath.Join(cfgDir, "config.yml"))
	s.Require().NoError(err)
	f.Close()
	s.TelepresenceConnect(ctx)
	itest.TelepresenceQuitOk(ctx)
}
