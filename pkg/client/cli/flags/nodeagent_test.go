package flags

import (
	"context"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	empty "google.golang.org/protobuf/types/known/emptypb"

	"github.com/telepresenceio/telepresence/rpc/v2/connector"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/daemon"
	"github.com/telepresenceio/telepresence/v2/pkg/json"
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

// fakeSessionUserClient is a minimal daemon.UserClient that only implements
// GetConfig, returning a canned *connector.ClientConfig or error. Embedding
// daemon.UserClient satisfies the interface without implementing every
// method, mirroring the pattern used in intercept/command_test.go.
type fakeSessionUserClient struct {
	daemon.UserClient
	cc  *connector.ClientConfig
	err error
}

func (f fakeSessionUserClient) GetConfig(context.Context, *empty.Empty, ...grpc.CallOption) (*connector.ClientConfig, error) {
	return f.cc, f.err
}

// sessionConfigJSON marshals a client.SessionConfig carrying the given
// nodeAgent.enabled value the same way the userd's Connector.GetConfig RPC
// does, for use as a fake GetConfig response.
func sessionConfigJSON(t *testing.T, nodeAgentEnabled bool) []byte {
	t.Helper()
	cfg := client.GetDefaultConfig()
	cfg.NodeAgent().Enabled = nodeAgentEnabled
	data, err := json.Marshal(&client.SessionConfig{Config: cfg})
	require.NoError(t, err)
	return data
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

	t.Run("session config enabled, local config disabled, flag absent -> true", func(t *testing.T) {
		// The session-merged (cluster) config wins over the local config
		// when the flag wasn't passed explicitly.
		cmd := newNodeAgentTestCommand(t, false)
		uc := fakeSessionUserClient{cc: &connector.ClientConfig{Json: sessionConfigJSON(t, true)}}
		cmd.SetContext(daemon.WithUserClient(cmd.Context(), uc))
		require.NoError(t, cmd.ParseFlags(nil))
		assert.True(t, NodeAgentDefault(cmd, false))
	})

	t.Run("session config disabled, local config enabled, flag absent -> false", func(t *testing.T) {
		// A session-merged config that reports disabled wins over an
		// enabled local-only value, since the session config already
		// reflects the local config.yml (whichever wins the cluster/local
		// merge implemented by (*config).DestructiveMerge).
		cmd := newNodeAgentTestCommand(t, true)
		uc := fakeSessionUserClient{cc: &connector.ClientConfig{Json: sessionConfigJSON(t, false)}}
		cmd.SetContext(daemon.WithUserClient(cmd.Context(), uc))
		require.NoError(t, cmd.ParseFlags(nil))
		assert.False(t, NodeAgentDefault(cmd, false))
	})

	t.Run("explicit --node-agent=false overrides an enabled session config", func(t *testing.T) {
		cmd := newNodeAgentTestCommand(t, false)
		uc := fakeSessionUserClient{cc: &connector.ClientConfig{Json: sessionConfigJSON(t, true)}}
		cmd.SetContext(daemon.WithUserClient(cmd.Context(), uc))
		require.NoError(t, cmd.ParseFlags([]string{"--node-agent=false"}))
		flagValue, err := cmd.Flags().GetBool("node-agent")
		require.NoError(t, err)
		assert.False(t, NodeAgentDefault(cmd, flagValue))
	})

	t.Run("session RPC error falls back to local config", func(t *testing.T) {
		cmd := newNodeAgentTestCommand(t, true)
		uc := fakeSessionUserClient{err: assert.AnError}
		cmd.SetContext(daemon.WithUserClient(cmd.Context(), uc))
		require.NoError(t, cmd.ParseFlags(nil))
		assert.True(t, NodeAgentDefault(cmd, false))
	})

	t.Run("unparsable session config falls back to local config", func(t *testing.T) {
		cmd := newNodeAgentTestCommand(t, true)
		uc := fakeSessionUserClient{cc: &connector.ClientConfig{Json: []byte("not json")}}
		cmd.SetContext(daemon.WithUserClient(cmd.Context(), uc))
		require.NoError(t, cmd.ParseFlags(nil))
		assert.True(t, NodeAgentDefault(cmd, false))
	})

	t.Run("no user client in context falls back to local config", func(t *testing.T) {
		cmd := newNodeAgentTestCommand(t, true)
		require.NoError(t, cmd.ParseFlags(nil))
		assert.True(t, NodeAgentDefault(cmd, false))
	})
}
