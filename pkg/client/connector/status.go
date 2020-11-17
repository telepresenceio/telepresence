package connector

import (
	"github.com/datawire/ambassador/pkg/supervisor"

	manager "github.com/datawire/telepresence2/pkg/rpc"
	rpc "github.com/datawire/telepresence2/pkg/rpc/connector"
)

// status reports the current status of the daemon
func (s *service) status(_ *supervisor.Process) *rpc.ConnectorStatus {
	r := &rpc.ConnectorStatus{}
	if s.cluster == nil {
		r.Error = rpc.ConnectorStatus_DISCONNECTED
		return r
	}
	r.Cluster = &rpc.ConnectorStatus_ClusterInfo{
		Connected: s.cluster.IsOkay(),
		Server:    s.cluster.server(),
		Context:   s.cluster.Context,
	}
	r.Bridge = s.cluster.isBridgeOkay()

	if s.trafficMgr == nil {
		return r
	}

	if !s.trafficMgr.IsOkay() {
		r.Intercepts = &manager.InterceptInfoSnapshot{}
		r.Agents = &manager.AgentInfoSnapshot{}
		if err := s.trafficMgr.apiErr; err != nil {
			r.ErrorText = err.Error()
		}
	} else {
		r.Agents = s.trafficMgr.agentInfoSnapshot()
		r.Intercepts = s.trafficMgr.interceptInfoSnapshot()
	}
	return r
}
