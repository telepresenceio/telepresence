package main

import (
	"github.com/datawire/teleproxy/pkg/supervisor"
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
	out.Printf("  Interceptable: %d deployments\n", len(d.interceptables))
	out.Printf("  Intercepts:    ? total, %d local\n", len(d.intercepts))
	return nil
}
