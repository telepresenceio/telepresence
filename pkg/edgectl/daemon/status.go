package daemon

import (
	"github.com/datawire/ambassador/pkg/api/edgectl/rpc"
	"github.com/datawire/ambassador/pkg/supervisor"
)

// status reports the current status of the daemon
func (d *daemon) status(_ *supervisor.Process) *rpc.StatusResponse {
	r := &rpc.StatusResponse{}
	if d.network == nil {
		r.Error = rpc.StatusResponse_Paused
		return r
	}
	if !d.network.IsOkay() {
		r.Error = rpc.StatusResponse_NoNetwork
		return r
	}
	if d.cluster == nil {
		r.Error = rpc.StatusResponse_Disconnected
		return r
	}
	r.Cluster = &rpc.StatusResponse_ClusterInfo{
		Connected: d.cluster.IsOkay(),
		Server:    d.cluster.Server(),
		Context:   d.cluster.Context(),
	}
	r.Bridge = d.bridge != nil && d.bridge.IsOkay()

	if d.trafficMgr == nil {
		return r
	}

	if !d.trafficMgr.IsOkay() {
		r.Intercepts = &rpc.StatusResponse_InterceptsInfo{Connected: false}
		if err := d.trafficMgr.apiErr; err != nil {
			r.ErrorText = err.Error()
		}
	} else {
		r.Intercepts = &rpc.StatusResponse_InterceptsInfo{
			Connected:          true,
			InterceptableCount: int32(len(d.trafficMgr.interceptables)),
			ClusterIntercepts:  int32(d.trafficMgr.totalClusCepts),
			LocalIntercepts:    int32(len(d.intercepts)),
			LicenseInfo:        d.trafficMgr.licenseInfo,
		}
	}
	return r
}
