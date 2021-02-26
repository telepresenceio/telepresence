package client

import (
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
images:
  registry: testregistry.io
  agentImage: ambassador-telepresence-client-image:0.0.1
  webhookAgentImage: ambassador-telepresence-webhook-image:0.0.2
`,
	}

	tmp := t.TempDir()
	sys1 := filepath.Join(tmp, "sys1")
	sys2 := filepath.Join(tmp, "sys2")
	user := filepath.Join(tmp, "user")
	for i, dir := range []string{sys1, sys2, user} {
		require.NoError(t, os.MkdirAll(dir, 0700))
		require.NoError(t, os.WriteFile(filepath.Join(dir, configFile), []byte(configs[i]), 0600))
	}

	c := dlog.NewTestContext(t, false)
	c = filelocation.WithAppSystemConfigDirs(c, []string{sys1, sys2})
	c = filelocation.WithAppUserConfigDir(c, user)
	env, err := LoadEnv(c)
	require.NoError(t, err)
	c = WithEnv(c, env)

	cfg, err := LoadConfig(c)
	require.NoError(t, err)
	c = WithConfig(c, cfg)

	cfg = GetConfig(c)
	to := &cfg.Timeouts
	assert.Equal(t, 2*time.Minute+10*time.Second, to.PrivateAgentInstall) // from sys1
	assert.Equal(t, 33*time.Second, to.PrivateApply)                      // from sys2
	assert.Equal(t, 25*time.Second, to.PrivateClusterConnect)             // from user
	assert.Equal(t, 17*time.Second, to.PrivateProxyDial)                  // from user

	assert.Equal(t, logrus.DebugLevel, cfg.LogLevels.UserDaemon) // from sys2
	assert.Equal(t, logrus.TraceLevel, cfg.LogLevels.RootDaemon) // from user

	assert.Equal(t, "testregistry.io", cfg.Images.Registry)                                      // from user
	assert.Equal(t, "ambassador-telepresence-client-image:0.0.1", cfg.Images.AgentImage)         // from user
	assert.Equal(t, "ambassador-telepresence-webhook-image:0.0.2", cfg.Images.WebhookAgentImage) // from user
}
