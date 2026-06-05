package client

import (
	"context"
	"log/slog"
	"net/netip"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/telepresenceio/clog"
	"github.com/telepresenceio/clog/testutil"
	"github.com/telepresenceio/telepresence/v2/pkg/filelocation"
)

func TestGetConfig(t *testing.T) {
	config := `
timeouts:
  clusterConnect: 25s
  proxyDial: 17s
logLevels:
  rootDaemon: trace
dns:
  recursionCheck: true
images:
  registry: testregistry.io
  agentImage: ambassador-telepresence-agent-image:0.0.2
  clientImage: ambassador-telepresence-image:0.0.2
intercept:
  defaultPort: 9080
  useFtp: true
routing:
  virtualSubnet: 192.169.0.0/16
`

	tmp := t.TempDir()
	user := filepath.Join(tmp, "user")
	require.NoError(t, os.MkdirAll(user, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(user, ConfigFile), []byte(config), 0o600))

	c := testutil.NewContext(t, false)
	c = filelocation.WithAppUserConfigDir(c, user)
	// Pin the system config dir to an empty temp dir so the test never picks
	// up an installer-written config on the developer's host.
	c = filelocation.WithAppSystemConfigDir(c, filepath.Join(tmp, "system"))
	env, err := LoadEnv()
	require.NoError(t, err)
	c = WithEnv(c, &env)

	cfg, err := LoadConfig(c)
	require.NoError(t, err)
	c = WithConfig(c, cfg)

	cfg = GetConfig(c)
	to := cfg.Timeouts()
	assert.Equal(t, 25*time.Second, to.PrivateClusterConnect)    // from user
	assert.Equal(t, 17*time.Second, to.PrivateProxyDial)         // from user
	assert.Equal(t, clog.LevelTrace, cfg.LogLevels().RootDaemon) // from user

	assert.Equal(t, "testregistry.io", cfg.Images().PrivateRegistry)                             // from user
	assert.Equal(t, "ambassador-telepresence-agent-image:0.0.2", cfg.Images().PrivateAgentImage) // from user
	assert.Equal(t, "ambassador-telepresence-image:0.0.2", cfg.Images().PrivateClientImage)      // from user
	assert.Equal(t, 9080, cfg.Intercept().DefaultPort)                                           // from user
	assert.True(t, cfg.Intercept().UseFtp)                                                       // from user
	assert.True(t, cfg.DNS().RecursionCheck)                                                     // from user
	assert.Equal(t, cfg.Routing().VirtualSubnet, netip.MustParsePrefix("192.169.0.0/16"))        // from user
}

// TestLoadConfig_SystemAndUserMerge verifies that LoadConfig reads the
// machine-wide config first and then DestructiveMerges the per-user config
// on top: a user's non-default value overrides the system's, while the
// system's non-default values survive when the user doesn't override them.
func TestLoadConfig_SystemAndUserMerge(t *testing.T) {
	tmp := t.TempDir()
	sys := filepath.Join(tmp, "system")
	user := filepath.Join(tmp, "user")
	require.NoError(t, os.MkdirAll(sys, 0o755))
	require.NoError(t, os.MkdirAll(user, 0o700))

	// System config disables usage (the admin policy), sets a non-default
	// DNS recursion-check, and seeds an intercept port. The user does not
	// override the first two, so they must survive the merge.
	const sysYaml = `
usage:
  enabled: false
dns:
  recursionCheck: true
intercept:
  defaultPort: 9080
`
	require.NoError(t, os.WriteFile(filepath.Join(sys, ConfigFile), []byte(sysYaml), 0o644))

	// User config sets a non-default intercept port (overriding the system)
	// and a timeout the system did not set.
	const userYaml = `
intercept:
  defaultPort: 9090
timeouts:
  clusterConnect: 30s
`
	require.NoError(t, os.WriteFile(filepath.Join(user, ConfigFile), []byte(userYaml), 0o600))

	c := testutil.NewContext(t, false)
	c = filelocation.WithAppSystemConfigDir(c, sys)
	c = filelocation.WithAppUserConfigDir(c, user)
	env, err := LoadEnv()
	require.NoError(t, err)
	c = WithEnv(c, &env)

	cfg, err := LoadConfig(c)
	require.NoError(t, err)

	// System non-default values survive when the user did not override:
	assert.False(t, cfg.Usage().Enabled, "system opt-out must survive when user didn't override")
	assert.True(t, cfg.DNS().RecursionCheck)
	// User non-default overrides the system's non-default:
	assert.Equal(t, 9090, cfg.Intercept().DefaultPort)
	// User-only value lands:
	assert.Equal(t, 30*time.Second, cfg.Timeouts().PrivateClusterConnect)
}

// TestLoadConfig_PinnedFileSkipsSystemConfig verifies that a caller that
// pinned a specific config path via WithConfigFile (the rootd does this)
// reads ONLY that file and does not silently merge anything from the
// machine-wide location.
func TestLoadConfig_PinnedFileSkipsSystemConfig(t *testing.T) {
	tmp := t.TempDir()
	sys := filepath.Join(tmp, "system")
	require.NoError(t, os.MkdirAll(sys, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(sys, ConfigFile),
		[]byte("usage:\n  enabled: false\n"), 0o644))

	pinned := filepath.Join(tmp, "rootd.yml")
	require.NoError(t, os.WriteFile(pinned, []byte("timeouts:\n  clusterConnect: 45s\n"), 0o600))

	c := testutil.NewContext(t, false)
	c = filelocation.WithAppSystemConfigDir(c, sys)
	c = WithConfigFile(c, pinned)
	env, err := LoadEnv()
	require.NoError(t, err)
	c = WithEnv(c, &env)

	cfg, err := LoadConfig(c)
	require.NoError(t, err)
	assert.Equal(t, 45*time.Second, cfg.Timeouts().PrivateClusterConnect)
	assert.True(t, cfg.Usage().Enabled, "pinned file must not pick up system opt-out")
}

func Test_ConfigMarshalYAML(t *testing.T) {
	ctx := testutil.NewContext(t, true)
	env, err := LoadEnv()
	require.NoError(t, err)
	ctx = WithEnv(ctx, &env)
	cfg := GetDefaultConfig()
	cfg.Images().PrivateAgentImage = "something:else"
	cfg.Timeouts().PrivateTrafficManagerAPI = defaultTimeoutsTrafficManagerAPI + 20*time.Second
	cfg.LogLevels().UserDaemon = clog.LevelTrace
	cfg.Grpc().MaxReceiveSizeV, _ = resource.ParseQuantity("20Mi")
	cfg.Intercept().DefaultPort = 9080
	cfg.Cluster().DefaultManagerNamespace = "hello-there"
	cfgBytes, err := cfg.MarshalYAML()
	require.NoError(t, err)

	// Store YAML in file
	tmp := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(tmp, ConfigFile), cfgBytes, 0o600))
	ctx = filelocation.WithAppUserConfigDir(ctx, tmp)

	// Load from file and compare
	cfg2, err := LoadConfig(ctx)
	require.NoError(t, err)
	require.Equal(t, cfg, cfg2)
}

func Test_ConfigMarshalYAMLDefaults(t *testing.T) {
	cfgBytes, err := GetDefaultConfig().MarshalYAML()
	require.NoError(t, err)
	require.Equal(t, "{}\n", string(cfgBytes))
}

func Test_ConfigUnmarshalYAMLEmpty(t *testing.T) {
	cfg, err := ParseConfigYAML(testutil.NewContext(t, true), "", []byte("{}"))
	require.NoError(t, err)
	require.Equal(t, GetDefaultConfig(), cfg)
}

func Test_ConfigUnmarshalYAMLBooleanTrueDefault(t *testing.T) {
	cfg, err := ParseConfigYAML(testutil.NewContext(t, true), "", []byte(`
docker:
  enableIPv4: true
`))
	require.NoError(t, err)
	require.Equal(t, GetDefaultConfig(), cfg)
}

func Test_ConfigUnmarshalYAMLBooleanTrueDefaultFalse(t *testing.T) {
	cfg, err := ParseConfigYAML(testutil.NewContext(t, true), "", []byte(`
docker:
  enableIPv4: false
`))
	require.NoError(t, err)
	require.NotEqual(t, GetDefaultConfig(), cfg)
}

func Test_ConfigUnmarshalYAMLEmptyParent(t *testing.T) {
	cfg, err := ParseConfigYAML(testutil.NewContext(t, true), "", []byte(`
docker:
`))
	require.NoError(t, err)
	require.Equal(t, GetDefaultConfig(), cfg)
}

func Test_ConfigMarshalYAMLDefaultsNotEmitted(t *testing.T) {
	cfg := GetDefaultConfig()
	lls := cfg.LogLevels()
	lls.UserDaemon = defaultLogLevels.UserDaemon
	lls.RootDaemon = slog.LevelDebug
	cfgBytes, err := cfg.MarshalYAML()
	require.NoError(t, err)
	require.Equal(t, "logLevels:\n  rootDaemon: DEBUG\n", string(cfgBytes))
}

func Test_ConfigUnmarshalUnsupported(t *testing.T) {
	config := []byte(`---
logLevels:
  userDaemon: debug
  fooDaemon: debug
`)
	cfg, err := ParseConfigYAML(context.Background(), "config.yml", config)
	require.NoError(t, err)
	require.Equal(t, cfg.LogLevels().UserDaemon, slog.LevelDebug)
}
