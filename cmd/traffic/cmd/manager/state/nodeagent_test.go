package state

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	rpc "github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/managerutil"
	"github.com/telepresenceio/telepresence/v2/pkg/errcat"
)

// TestNodeAgentGateErr_Disabled verifies that requesting node-agent mode
// while the traffic-manager has not enabled it produces a User-categorized
// error, which is the gate PrepareIntercept applies before ever attempting
// to provision a node-hosted agent.
func TestNodeAgentGateErr_Disabled(t *testing.T) {
	t.Parallel()

	env := &managerutil.Env{NodeAgentEnabled: false}
	err := nodeAgentGateErr(env)
	require.Error(t, err)
	assert.Equal(t, errcat.User, errcat.GetCategory(err))
	assert.Contains(t, err.Error(), "node-agent mode is not enabled")
}

// TestNodeAgentGateErr_Enabled verifies that the gate lets requests through
// once node-agent mode is enabled on the traffic-manager.
func TestNodeAgentGateErr_Enabled(t *testing.T) {
	t.Parallel()

	env := &managerutil.Env{NodeAgentEnabled: true}
	assert.NoError(t, nodeAgentGateErr(env))
}

// TestEnsureNodeAgent_NotYetImplemented verifies that, once the gate has been
// passed, the provisioning seam reports a User-categorized "not yet
// implemented" error rather than attempting (and failing at) real Job
// construction, which is left for a later change.
func TestEnsureNodeAgent_NotYetImplemented(t *testing.T) {
	t.Parallel()

	s := &State{}
	spec := &rpc.InterceptSpec{NodeAgent: true, Agent: "test-agent", Namespace: "test-namespace"}
	client := &ClientSession{ClientInfo: &rpc.ClientInfo{Name: "client-name"}}

	pi, err := s.ensureNodeAgent(t.Context(), nil, spec, client)
	require.Nil(t, pi)
	require.Error(t, err)
	assert.Equal(t, errcat.User, errcat.GetCategory(err))
	assert.Contains(t, err.Error(), "node-agent provisioning is not yet implemented")
}
