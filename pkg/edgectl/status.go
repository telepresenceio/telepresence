package edgectl

import (
	"github.com/datawire/ambassador/pkg/supervisor"
)

// Status reports the current status of the daemon
func (d *Daemon) Status(_ *supervisor.Process, out *Emitter) error {
	out.Send("paused", d.network == nil)
	if d.network == nil {
		out.Println("Network overrides are paused")
		return nil
	}
	if !d.network.IsOkay() {
		out.Println("Network overrides NOT established")
	}
	out.Send("net_overrides", d.network.IsOkay())
	if d.cluster == nil {
		out.Println("Not connected (use 'edgectl connect' to connect to your cluster)")
		out.Send("cluster", nil)
		return nil
	}
	if d.cluster.IsOkay() {
		out.Println("Connected")
		out.Send("cluster.connected", true)
	} else {
		out.Println("Attempting to reconnect...")
		out.Send("cluster.connected", false)
	}
	out.Printf("  Context:       %s (%s)\n", d.cluster.Context(), d.cluster.Server())
	out.Send("cluster.context", d.cluster.Context())
	out.Send("cluster.server", d.cluster.Server())
	if d.bridge != nil && d.bridge.IsOkay() {
		out.Println("  Proxy:         ON (networking to the cluster is enabled)")
		out.Send("bridge", true)
	} else {
		out.Println("  Proxy:         OFF (attempting to connect...)")
		out.Send("bridge", false)
	}
	switch {
	case d.trafficMgr == nil:
		out.Println("  Intercepts:    Unavailable: no traffic manager")
		out.Send("intercept", nil)
	case !d.trafficMgr.IsOkay():
		if d.trafficMgr.apiErr != nil {
			out.Printf("  Intercepts:    %s\n", d.trafficMgr.apiErr.Error())
		} else {
			out.Println("  Intercepts:    (connecting to traffic manager...)")
		}
		out.Send("intercept.connected", false)
	default:
		out.Send("intercept.connected", true)
		out.Printf("  Interceptable: %d deployments\n", len(d.trafficMgr.interceptables))
		out.Printf("  Intercepts:    %d total, %d local\n", d.trafficMgr.totalClusCepts, len(d.intercepts))
		if d.trafficMgr.licenseInfo != "" {
			out.Println(d.trafficMgr.licenseInfo)
		}
		out.Send("interceptable", len(d.trafficMgr.interceptables))
		out.Send("cluster_intercepts", d.trafficMgr.totalClusCepts)
		out.Send("local_intercepts", len(d.intercepts))
	}
	return nil
}
