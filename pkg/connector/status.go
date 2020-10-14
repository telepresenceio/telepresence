package connector

import (
	"github.com/datawire/ambassador/pkg/supervisor"
	"github.com/datawire/telepresence2/pkg/rpc"
)

// status reports the current status of the daemon
func (s *service) status(_ *supervisor.Process) *rpc.ConnectorStatusResponse {
	r := &rpc.ConnectorStatusResponse{}
	if s.cluster == nil {
		r.Error = rpc.ConnectorStatusResponse_Disconnected
		return r
	}
	r.Cluster = &rpc.ConnectorStatusResponse_ClusterInfo{
		Connected: s.cluster.IsOkay(),
		Server:    s.cluster.server(),
		Context:   s.cluster.context(),
	}
	r.Bridge = s.bridge != nil && s.bridge.IsOkay()

	if s.trafficMgr == nil {
		return r
	}

	if !s.trafficMgr.IsOkay() {
		r.Intercepts = &rpc.ConnectorStatusResponse_InterceptsInfo{Connected: false}
		if err := s.trafficMgr.apiErr; err != nil {
			r.ErrorText = err.Error()
		}
	} else {
		r.Intercepts = &rpc.ConnectorStatusResponse_InterceptsInfo{
			Connected:          true,
			InterceptableCount: int32(len(s.trafficMgr.interceptables)),
			ClusterIntercepts:  int32(s.trafficMgr.totalClusCepts),
			LocalIntercepts:    int32(len(s.intercepts)),
			LicenseInfo:        s.trafficMgr.licenseInfo,
		}
	}
	return r
}
