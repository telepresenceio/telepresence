package docker

import (
	"testing"

	"github.com/blang/semver/v4"
	"github.com/stretchr/testify/require"

	"github.com/telepresenceio/dlib/v2/dlog"
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

	di := client.DockerImage(cfg.Docker().Telemount)
	ver, err := getLatestPluginVersion(c, pluginName(&di), &di)
	require.NoError(t, err)
	require.True(t, ver.EQ(zeroVersion) || semver.MustParse("0.1.3").LT(ver))
}
