package quic

import (
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/cli"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/managers"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/workloads"
)

// disableAgentPortForward is the rt.ConnWithConfig delta
// Test_RelaysThroughManager applies: VPN-only mode
// (pkg/client/config.go's Cluster.AgentPortForward), no direct
// client-to-agent port-forwards.
func disableAgentPortForward(c client.Config) {
	c.Cluster().AgentPortForward = false
}

// Relay proves the VPN-only relay path managers.QuicRelay() exists for:
// with the connecting client's own cluster.agentPortForward=false, a
// workload that already carries an injected traffic-agent sidecar is still
// reachable, forced to relay agent-bound traffic through the manager -- and
// therefore over the quic transport, since QuicRelay's manager spec is the
// same managers.QuicNodePort() install every other suite in this area uses.
// Intercepts themselves are unavailable with agentPortForward=false
// (pkg/client/userd/trafficmgr/intercept.go's requireAgentPortForward), so
// the sidecar is injected up front by a short-lived intercept on a normal
// connection, detached, and only then does the suite reconnect under the
// relay config.
type Relay struct {
	rt.Suite
}

func init() {
	rt.Register(&Relay{}, rt.InArea("quic"), rt.NeedsManager(managers.QuicRelay()))
}

func (s *Relay) Test_RelaysThroughManager() {
	t := s.T()
	ctx := s.Ctx()
	ns := s.AppNamespace()
	wl := s.Workload(workloads.Echo("quic-relay"))
	ls := s.LocalEcho()

	conn := freshConnect(t, ctx, ns)
	a := conn.Intercept(t, wl, rt.ToLocal(ls, "http"), cli.MountFalse())
	a.Detach(t)

	// provisionConnection's ensureHostDaemon quits the daemon above before
	// connecting fresh under the relay config; no explicit disconnect needed
	// (mirrors intercept/routing.go's Test_LocalShortcut).
	rt.Mutate(t, rt.ConnectionFixture(ns, rt.ConnWithConfig(disableAgentPortForward)))

	// The sidecar is still in the pod; without agent port-forwards, the only
	// path to it is the manager relay.
	rt.RoutedToCluster(t, wl.ServiceURL())
}
