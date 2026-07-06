package state

import (
	"context"

	rpc "github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/managerutil"
	"github.com/telepresenceio/telepresence/v2/pkg/errcat"
	"github.com/telepresenceio/telepresence/v2/pkg/k8sapi"
)

// nodeAgentGateErr returns a user error when node-agent mode has been
// requested for an intercept but the traffic-manager does not have it
// enabled, and nil otherwise.
func nodeAgentGateErr(env *managerutil.Env) error {
	if !env.NodeAgentEnabled {
		return errcat.User.New("node-agent mode is not enabled on this traffic-manager")
	}
	return nil
}

// ensureNodeAgent provisions a node-hosted traffic-agent (a manager-created
// Job that enters the target pod's namespaces) for the workload and returns
// the PreparedIntercept describing it. The Job construction and lifecycle are
// implemented in a later change; this seam currently reports that the mode is
// not yet available.
//
//nolint:unparam // seam: parameters are consumed once Job construction lands in a later change
func (s *State) ensureNodeAgent(
	ctx context.Context,
	wl k8sapi.Workload,
	spec *rpc.InterceptSpec,
	client *ClientSession,
) (*rpc.PreparedIntercept, error) {
	return nil, errcat.User.New("node-agent provisioning is not yet implemented")
}
