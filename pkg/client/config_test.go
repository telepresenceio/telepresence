package client

import (
	"io/ioutil"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/pkg/filelocation"
)

func TestGetConfig(t *testing.T) {
	configs := []string{
		/* sys1 */ `
timeouts:
  agentInstall: 2m10s
logLevels:
  userDaemon: info
  rootDaemon: debug
`,
		/* sys2 */ `
timeouts:
  apply: 33s
logLevels:
  userDaemon: debug
`,
		/* user */ `
timeouts:
  clusterConnect: 25
  proxyDial: 17.0
logLevels:
  rootDaemon: trace
`,
	}

	tmp := t.TempDir()
	sys1 := filepath.Join(tmp, "sys1")
	sys2 := filepath.Join(tmp, "sys2")
	user := filepath.Join(tmp, "user")
	for i, dir := range []string{sys1, sys2, user} {
		require.NoError(t, os.MkdirAll(dir, 0700))
		require.NoError(t, ioutil.WriteFile(filepath.Join(dir, configFile), []byte(configs[i]), 0600))
	}

	c := dlog.NewTestContext(t, false)
	c = filelocation.WithAppSystemConfigDirs(c, []string{sys1, sys2})
	c = filelocation.WithAppUserConfigDir(c, user)

	cfg := GetConfig(c)
	to := &cfg.Timeouts
	assert.Equal(t, 2*time.Minute+10*time.Second, to.PrivateAgentInstall) // from sys1
	assert.Equal(t, 33*time.Second, to.PrivateApply)                      // from sys2
	assert.Equal(t, 25*time.Second, to.PrivateClusterConnect)             // from user
	assert.Equal(t, 17*time.Second, to.PrivateProxyDial)                  // from user
	assert.Equal(t, defaultConfig.Timeouts.PrivateIntercept, to.PrivateIntercept)
	assert.Equal(t, defaultConfig.Timeouts.PrivateTrafficManagerConnect, to.PrivateTrafficManagerConnect)

	assert.Equal(t, logrus.DebugLevel, cfg.LogLevels.UserDaemon) // from sys2
	assert.Equal(t, logrus.TraceLevel, cfg.LogLevels.RootDaemon) // from user
}
