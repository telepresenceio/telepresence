package connector

import (
	"github.com/datawire/ambassador/pkg/supervisor"

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
		Context:   s.cluster.context(),
	}
	r.Bridge = s.cluster.isBridgeOkay()

	if s.trafficMgr == nil {
		return r
	}

	if !s.trafficMgr.IsOkay() {
		r.Intercepts = &rpc.ConnectorStatus_InterceptsInfo{Connected: false}
		if err := s.trafficMgr.apiErr; err != nil {
			r.ErrorText = err.Error()
		}
	} else {
		r.Intercepts = &rpc.ConnectorStatus_InterceptsInfo{
			Connected:          true,
			InterceptableCount: int32(len(s.trafficMgr.interceptables)),
			ClusterIntercepts:  int32(s.trafficMgr.totalClusCepts),
			LocalIntercepts:    int32(len(s.intercepts)),
			LicenseInfo:        s.trafficMgr.licenseInfo,
		}
	}
	return r
}
