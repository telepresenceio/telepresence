package docker

import (
	"testing"

	"github.com/blang/semver"
	"github.com/stretchr/testify/require"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
)

func Test_getLatestPluginVersion(t *testing.T) {
	c := dlog.NewTestContext(t, false)
	env, err := client.LoadEnv()
	require.NoError(t, err)
	c = client.WithEnv(c, env)

	cfg, err := client.LoadConfig(c)
	require.NoError(t, err)
	c = client.WithConfig(c, cfg)

	ver, err := getLatestPluginVersion(c, pluginName(c))
	require.NoError(t, err)
	require.True(t, semver.MustParse("0.1.3").LT(ver))
}
