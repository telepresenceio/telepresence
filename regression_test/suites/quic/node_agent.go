package quic

import (
	"strings"

	"github.com/telepresenceio/telepresence/v2/regression_test/framework/cli"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/managers"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/workloads"
)

// nodeAgentFlag builds --node-agent, requesting a node-hosted traffic-agent
// Job instead of an injected sidecar for the attach.
func nodeAgentFlag() cli.InterceptOpt {
	return func() []string { return []string{"--node-agent"} }
}

// NodeAgentTransport proves a node-hosted traffic-agent (a --node-agent
// attach) also rides the quic transport, not just an injected sidecar. It
// lives in this area rather than nodeagent because this area owns the
// managers.QuicNodePort() spec (its NodeAgent.Enabled is already true, see
// the spec's doc comment), and it uses its own dedicated workload because a
// node-agent attach refuses a workload that already carries an injected
// sidecar, which every other suite in this area injects into its own
// workload.
type NodeAgentTransport struct {
	rt.Suite
}

func init() {
	rt.Register(&NodeAgentTransport{}, rt.InArea("quic"), rt.NeedsManager(managers.QuicNodePort()))
}

func (s *NodeAgentTransport) Test_NodeAgentTransport() {
	t := s.T()
	ctx := s.Ctx()
	ns := s.AppNamespace()
	tp := s.CLI()
	wl := s.Workload(workloads.Echo("quic-node-agent"))
	ls := s.LocalEcho()

	conn := awaitTransportPrefix(t, ctx, tp, ns, freshConnect(t, ctx, ns))

	a := conn.Intercept(t, wl, rt.ToLocal(ls, "http"), cli.MountFalse(), nodeAgentFlag())
	defer a.Detach(t)
	rt.RoutedToLocal(t, wl.ServiceURL(), ls)

	// The client-to-agent attachment itself rides quic too, whether the
	// agent is a node-hosted Job or an injected sidecar.
	awaitAgentTransport(t, ctx, tp, wl.Name)

	st := fetchStatus(t, ctx, tp)
	s.True(strings.HasPrefix(st.RootDaemon.TunnelTransport, quicPrefix),
		"status should still report the quic transport with an active node-agent attach, got %q",
		st.RootDaemon.TunnelTransport)
}
