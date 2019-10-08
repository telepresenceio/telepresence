package main

import (
	"github.com/datawire/ambassador/pkg/supervisor"
)

// Status reports the current status of the daemon
func (d *Daemon) Status(_ *supervisor.Process, out *Emitter) error {
	if !d.network.IsOkay() {
		out.Println("Network overrides NOT established")
	}
	if d.cluster == nil {
		out.Println("Not connected")
		return nil
	}
	if d.cluster.IsOkay() {
		out.Println("Connected")
	} else {
		out.Println("Attempting to reconnect...")
	}
	out.Printf("  Context:       %s (%s)\n", d.cluster.Context(), d.cluster.Server())
	if d.bridge != nil && d.bridge.IsOkay() {
		out.Println("  Proxy:         ON (networking to the cluster is enabled)")
	} else {
		out.Println("  Proxy:         OFF (attempting to connect...)")
	}
	switch {
	case d.trafficMgr == nil:
		out.Println("  Intercepts:    Unavailable: no traffic manager")
	case !d.trafficMgr.IsOkay():
		out.Println("  Intercepts:    (connecting to traffic manager...)")
	default:
		out.Printf("  Interceptable: %d deployments\n", len(d.trafficMgr.interceptables))
		out.Printf("  Intercepts:    %d total, %d local\n", d.trafficMgr.totalClusCepts, len(d.intercepts))
	}
	return nil
}
