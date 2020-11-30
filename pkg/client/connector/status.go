package connector

import (
	"context"

	rpc "github.com/datawire/telepresence2/pkg/rpc/connector"
	"github.com/datawire/telepresence2/pkg/rpc/manager"
)

// status reports the current status of the daemon
func (s *service) status(c context.Context) *rpc.ConnectorStatus {
	r := &rpc.ConnectorStatus{}
	if s.cluster == nil {
		r.Error = rpc.ConnectorStatus_DISCONNECTED
		return r
	}
	r.Cluster = &rpc.ConnectorStatus_ClusterInfo{
		Connected: true, // TODO: Better check
		Server:    s.cluster.server(),
		Context:   s.cluster.Context,
	}
	r.Bridge = s.bridge.check(c)

	if s.trafficMgr == nil {
		return r
	}
	if s.trafficMgr.grpc == nil {
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
