package cmd

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/telepresenceio/telepresence/v2/pkg/agentconfig"
)

func TestGenYAMLDurationJSON(t *testing.T) {
	output := filepath.Join(t.TempDir(), "config.yaml")
	cmd := genYAMLCommand{outputFile: output}
	require.NoError(t, cmd.writeObjToOutput(&agentconfig.Sidecar{
		ClientConnectionTTL: 24 * time.Hour,
		WatchRetryInterval:  3 * time.Second,
	}))

	data, err := os.ReadFile(output)
	require.NoError(t, err)
	text := string(data)
	require.Contains(t, text, "clientConnectionTTL: 24h0m0s")
	require.Contains(t, text, "watchRetryInterval: 3s")
}
