package flags

import (
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/telepresenceio/telepresence/v2/pkg/client"
)

// newNodeAgentTestCommand returns a cobra.Command with a --node-agent flag
// and a context carrying a client.Config whose nodeAgent.enabled is set to
// cfgEnabled, mirroring how intercept.Command and ingest.Command register
// the flag and how InitConfig seeds the context.
func newNodeAgentTestCommand(t *testing.T, cfgEnabled bool) *cobra.Command {
	t.Helper()
	cmd := &cobra.Command{Use: "test", RunE: func(*cobra.Command, []string) error { return nil }}
	var nodeAgent bool
	cmd.Flags().BoolVar(&nodeAgent, "node-agent", false, "")

	cfg := client.GetDefaultConfig()
	cfg.NodeAgent().Enabled = cfgEnabled
	cmd.SetContext(client.WithConfig(t.Context(), cfg))
	return cmd
}

func TestNodeAgentDefault(t *testing.T) {
	t.Run("config enabled, flag absent -> true", func(t *testing.T) {
		cmd := newNodeAgentTestCommand(t, true)
		require.NoError(t, cmd.ParseFlags(nil))
		assert.True(t, NodeAgentDefault(cmd, false))
	})

	t.Run("config enabled, explicit --node-agent=false -> false", func(t *testing.T) {
		cmd := newNodeAgentTestCommand(t, true)
		require.NoError(t, cmd.ParseFlags([]string{"--node-agent=false"}))
		flagValue, err := cmd.Flags().GetBool("node-agent")
		require.NoError(t, err)
		assert.False(t, NodeAgentDefault(cmd, flagValue))
	})

	t.Run("config disabled, flag absent -> false", func(t *testing.T) {
		cmd := newNodeAgentTestCommand(t, false)
		require.NoError(t, cmd.ParseFlags(nil))
		assert.False(t, NodeAgentDefault(cmd, false))
	})

	t.Run("config disabled, explicit --node-agent=true -> true", func(t *testing.T) {
		cmd := newNodeAgentTestCommand(t, false)
		require.NoError(t, cmd.ParseFlags([]string{"--node-agent=true"}))
		flagValue, err := cmd.Flags().GetBool("node-agent")
		require.NoError(t, err)
		assert.True(t, NodeAgentDefault(cmd, flagValue))
	})
}
