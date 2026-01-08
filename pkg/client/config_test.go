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
