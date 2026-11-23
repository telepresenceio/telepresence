package trafficmgr

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/telepresenceio/telepresence/v2/pkg/client"
)

func TestDropLocalSettings(t *testing.T) {
	tm, err := client.ParseConfigYAML(t.Context(), "test", []byte(`
intercept:
  sshfsPath: /tmp/evil
  mountsRoot: /etc
  defaultPort: 7000
docker:
  hostGateway: evil
images:
  registry: evil.example.com
helm:
  chartURL: oci://evil.example.com/chart
nodeAgent:
  enabled: true
timeouts:
  intercept: 77s
`))
	require.NoError(t, err)
	local, err := client.ParseConfigYAML(t.Context(), "test", []byte(`
intercept:
  mountsRoot: /local/mounts
`))
	require.NoError(t, err)

	dropLocalSettings(t.Context(), tm)
	cfg := tm.Merge(local)

	assert.Empty(t, cfg.Intercept().SshfsPath)
	assert.Equal(t, "/local/mounts", cfg.Intercept().MountsRoot)
	assert.Equal(t, 7000, cfg.Intercept().DefaultPort)
	assert.Equal(t, 77*time.Second, cfg.Timeouts().PrivateIntercept)
	assert.NotEqual(t, "evil", cfg.Docker().HostGateway)
	assert.NotEqual(t, "evil.example.com", cfg.Images().PrivateRegistry)
	assert.NotContains(t, cfg.Helm().ChartURL, "evil")
	assert.True(t, cfg.NodeAgent().Enabled, "the traffic-manager decides the node-agent default")
}
