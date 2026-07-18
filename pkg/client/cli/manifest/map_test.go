package manifest

import (
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/telepresenceio/telepresence/v2/pkg/client"
)

func testCmd(t *testing.T) *cobra.Command {
	t.Helper()
	cmd := &cobra.Command{Use: "test"}
	cmd.SetContext(client.WithConfig(t.Context(), client.GetDefaultConfig()))
	return cmd
}

func TestBuildInterceptCommand(t *testing.T) {
	t.Run("agent name defaults to attachment name", func(t *testing.T) {
		a := &Attachment{Type: TypeIntercept, Name: "echo-easy"}
		c, err := buildInterceptCommand(testCmd(t), a)
		require.NoError(t, err)
		assert.Equal(t, "echo-easy", c.Name)
		assert.Equal(t, "echo-easy", c.AgentName)
		assert.Equal(t, "tcp", c.Mechanism)
		assert.False(t, c.Wiretap)
	})

	t.Run("explicit workload overrides agent name", func(t *testing.T) {
		a := &Attachment{Type: TypeIntercept, Name: "echo-easy", Workload: "echo-server"}
		c, err := buildInterceptCommand(testCmd(t), a)
		require.NoError(t, err)
		assert.Equal(t, "echo-server", c.AgentName)
	})

	t.Run("HTTP header filter forces http mechanism", func(t *testing.T) {
		a := &Attachment{Type: TypeIntercept, Name: "echo-easy", HTTPHeaders: []string{"x-dev-user=thomas"}}
		c, err := buildInterceptCommand(testCmd(t), a)
		require.NoError(t, err)
		assert.Equal(t, "http", c.Mechanism)
	})

	t.Run("HTTP filter with UDP port is an error", func(t *testing.T) {
		a := &Attachment{
			Type:        TypeIntercept,
			Name:        "echo-easy",
			Ports:       []PortIdentifier{"8080/UDP"},
			HTTPHeaders: []string{"x-dev-user=thomas"},
		}
		_, err := buildInterceptCommand(testCmd(t), a)
		require.Error(t, err)
	})

	t.Run("invalid http header format is rejected", func(t *testing.T) {
		a := &Attachment{Type: TypeIntercept, Name: "echo-easy", HTTPHeaders: []string{"not-a-kv-pair"}}
		_, err := buildInterceptCommand(testCmd(t), a)
		require.Error(t, err)
	})

	t.Run("wiretap forces read-only mount", func(t *testing.T) {
		a := &Attachment{Type: TypeWiretap, Name: "echo-tap", Workload: "echo-server"}
		c, err := buildInterceptCommand(testCmd(t), a)
		require.NoError(t, err)
		assert.True(t, c.Wiretap)
		assert.True(t, c.MountFlags.ReadOnly)
	})

	t.Run("metadata is converted to key=value pairs", func(t *testing.T) {
		a := &Attachment{Type: TypeIntercept, Name: "echo-easy", Metadata: map[string]string{"owner": "thhal"}}
		c, err := buildInterceptCommand(testCmd(t), a)
		require.NoError(t, err)
		assert.Equal(t, []string{"owner=thhal"}, c.Metadata)
	})
}

func TestBuildReplaceCommand(t *testing.T) {
	t.Run("container property folds into name, agent stays bare", func(t *testing.T) {
		a := &Attachment{Type: TypeReplace, Name: "echo-server", Container: "svc"}
		c, err := buildReplaceCommand(testCmd(t), a)
		require.NoError(t, err)
		assert.Equal(t, "echo-server/svc", c.Name)
		assert.Equal(t, "echo-server", c.AgentName)
		assert.Equal(t, "svc", c.ContainerName)
	})

	t.Run("name already carrying /container is split for agent name", func(t *testing.T) {
		a := &Attachment{Type: TypeReplace, Name: "echo-server/svc"}
		c, err := buildReplaceCommand(testCmd(t), a)
		require.NoError(t, err)
		assert.Equal(t, "echo-server/svc", c.Name)
		assert.Equal(t, "echo-server", c.AgentName)
	})

	t.Run("ports default to all, mapped to :all", func(t *testing.T) {
		a := &Attachment{Type: TypeReplace, Name: "echo-server"}
		c, err := buildReplaceCommand(testCmd(t), a)
		require.NoError(t, err)
		assert.Equal(t, []string{":all"}, c.Ports)
	})

	t.Run("explicit all port is normalized the same way", func(t *testing.T) {
		a := &Attachment{Type: TypeReplace, Name: "echo-server", Ports: []PortIdentifier{"all"}}
		c, err := buildReplaceCommand(testCmd(t), a)
		require.NoError(t, err)
		assert.Equal(t, []string{":all"}, c.Ports)
	})

	t.Run("mechanism is always tcp and replace flags are set", func(t *testing.T) {
		a := &Attachment{Type: TypeReplace, Name: "echo-server"}
		c, err := buildReplaceCommand(testCmd(t), a)
		require.NoError(t, err)
		assert.Equal(t, "tcp", c.Mechanism)
		assert.True(t, c.Replace)
		assert.True(t, c.NoDefaultPort)
	})
}

func TestBuildIngestCommand(t *testing.T) {
	t.Run("maps name and container, forces read-only mount", func(t *testing.T) {
		a := &Attachment{Type: TypeIngest, Name: "echo-sidecar", Container: "logger"}
		c, err := buildIngestCommand(testCmd(t), a)
		require.NoError(t, err)
		assert.Equal(t, "echo-sidecar", c.WorkloadName)
		assert.Equal(t, "logger", c.ContainerName)
		assert.True(t, c.MountFlags.ReadOnly)
	})
}

func TestBuildMountFlags(t *testing.T) {
	t.Run("defaults to enabled with no explicit path", func(t *testing.T) {
		mf := buildMountFlags(&Attachment{}, false)
		assert.True(t, mf.Enabled)
		assert.Empty(t, mf.Mount)
		assert.False(t, mf.ReadOnly)
	})

	t.Run("disabled via mount.enabled=false", func(t *testing.T) {
		mf := buildMountFlags(&Attachment{Mount: &Mount{Enabled: new(false)}}, false)
		assert.False(t, mf.Enabled)
	})

	t.Run("forceReadOnly overrides an explicit false", func(t *testing.T) {
		mf := buildMountFlags(&Attachment{Mount: &Mount{ReadOnly: false}}, true)
		assert.True(t, mf.ReadOnly)
	})

	t.Run("explicit path and local mount port are carried over", func(t *testing.T) {
		mf := buildMountFlags(&Attachment{Mount: &Mount{Path: "/tmp/x", LocalMountPort: 1234}}, false)
		assert.Equal(t, "/tmp/x", mf.Mount)
		assert.Equal(t, uint16(1234), mf.LocalMountPort)
	})
}

func TestInterceptLookupName(t *testing.T) {
	assert.Equal(t, "echo-easy", interceptLookupName(&Attachment{Type: TypeIntercept, Name: "echo-easy"}))
	assert.Equal(t, "echo-server", interceptLookupName(&Attachment{Type: TypeWiretap, Name: "echo-server", Container: "svc"}))
	assert.Equal(t, "echo-server/svc", interceptLookupName(&Attachment{Type: TypeReplace, Name: "echo-server", Container: "svc"}))
	assert.Equal(t, "echo-server/svc", interceptLookupName(&Attachment{Type: TypeReplace, Name: "echo-server/svc"}))
}
