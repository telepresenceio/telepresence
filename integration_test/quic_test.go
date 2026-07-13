package integration_test

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/telepresenceio/telepresence/v2/integration_test/itest"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
)

// quicNodePort is the fixed NodePort used for the traffic-manager's QUIC
// tunnel Service in quicTunnelSuite. It has to be known before the Service
// exists, because it is both the Helm "quicTunnel.service.nodePort" value and
// (combined with the node's InternalIP) the "quicTunnel.externalPort"
// advertised to clients, so a random Kubernetes-assigned port won't do.
const quicNodePort = 30777

// quicTunnelSuite exercises the QUIC tunnel transport end-to-end against a
// traffic-manager installed with quicTunnel.enabled=true, a NodePort Service
// pinned to quicNodePort, and quicTunnel.externalHost set to the first node's
// InternalIP. It covers the matrix described in
// docs/plans/quic-transport/design.md: plain VPN-only traffic through the
// manager, a regular (sidecar) intercept coexisting with the transport
// upgrade, traffic to an intercepted workload forced to relay through the
// manager (cluster.agentPortForward=false), and a node-agent attach. All four
// are expected to run with "telepresence status" reporting the quic
// transport; see awaitQuicOrSkip for how this suite tells an environment that
// can't reach NodePorts (skip) apart from a QUIC endpoint that is reachable
// but never became the active transport (fail, a real bug).
//
// It embeds nodeAgentBase (node_agent_test.go) to reuse the node-agent
// environment gating and the assertNodeAgentIntercept assertion for
// Test_NodeAgentTransport, without duplicating that suite's full matrix.
type quicTunnelSuite struct {
	nodeAgentBase
	nodeIP   string // first node's InternalIP; used as quicTunnel.externalHost
	endpoint string // nodeIP:quicNodePort; the endpoint "telepresence status" is expected to report
}

func (s *quicTunnelSuite) SuiteName() string {
	return "QuicTunnel"
}

func init() {
	itest.AddNamespacePairSuite("", func(h itest.NamespacePair) itest.TestingSuite {
		return &quicTunnelSuite{nodeAgentBase: nodeAgentBase{Suite: itest.Suite{Harness: h}, NamespacePair: h}}
	})
}

// skipUnlessQuicTunnelSupported skips the current suite when the client or
// traffic-manager predates the QUIC tunnel transport.
func (s *quicTunnelSuite) skipUnlessQuicTunnelSupported() {
	if !(s.ManagerIsVersion(">2.29.x") && s.ClientIsVersion(">2.29.x")) {
		s.T().Skip("QUIC tunnel transport requires traffic-manager and client 2.30 or later")
	}
}

// discoverNodeIP returns the InternalIP of the cluster's first Node. It
// stands in for "an externally reachable host chosen by the cluster
// operator" (quicTunnel.externalHost): kind, minikube, k3d, and real cloud
// clusters all give the runner some route to a node's InternalIP, which is
// exactly what a NodePort Service is reachable on.
func (s *quicTunnelSuite) discoverNodeIP(ctx context.Context) string {
	out, err := itest.KubectlOut(ctx, "", "get", "nodes",
		"-o", `jsonpath={.items[0].status.addresses[?(@.type=="InternalIP")].address}`)
	s.Require().NoError(err, "failed to discover a node's InternalIP")
	// A dual-stack node reports one InternalIP per family, space-separated.
	ips := strings.Fields(out)
	s.Require().NotEmpty(ips, "no cluster node reported an InternalIP")
	return ips[0]
}

func (s *quicTunnelSuite) SetupSuite() {
	s.Suite.SetupSuite()
	s.skipUnlessQuicTunnelSupported()
	ctx := s.Context()

	// Defensive, like nodeAgentSuite/nodeAgentNoInjectorSuite: this suite
	// shares its manager namespace with theirs (same "" namespace-pair
	// suffix), and Test_NodeAgentTransport below creates node-agent Jobs of
	// its own.
	s.reapLeftoverNodeAgentJobs(ctx)

	s.nodeIP = s.discoverNodeIP(ctx)
	s.endpoint = net.JoinHostPort(s.nodeIP, strconv.Itoa(quicNodePort))

	s.TelepresenceHelmInstallOK(ctx, false,
		"--set", "nodeAgent.enabled=true",
		"--set", "quicTunnel.enabled=true",
		"--set", "quicTunnel.service.type=NodePort",
		"--set", fmt.Sprintf("quicTunnel.service.nodePort=%d", quicNodePort),
		"--set", "quicTunnel.externalHost="+s.nodeIP,
		"--set", fmt.Sprintf("quicTunnel.externalPort=%d", quicNodePort),
	)
	s.ApplyApp(ctx, "echo-easy", "deploy/echo-easy")
	s.TelepresenceConnect(ctx)

	// Fail fast (skip or require.Fail, see awaitQuicOrSkip) before running
	// any of the tests below if the endpoint never comes up.
	s.awaitQuicOrSkip(ctx)
}

func (s *quicTunnelSuite) TearDownSuite() {
	ctx := s.Context()
	itest.TelepresenceQuitOk(ctx)
	s.DeleteSvcAndWorkload(ctx, "deploy", "echo-easy")
	s.UninstallTrafficManager(ctx, s.ManagerNamespace())
}

// awaitQuicOrSkip polls "telepresence status" for the root daemon to report
// the quic transport with this suite's endpoint. The opportunistic dial
// resolves within quicDialTimeout (3s, see pkg/client/rootd/quic.go) of
// connect, so the window here only needs to cover scheduling jitter on top
// of that.
//
// If the transport never becomes "quic (<endpoint>)" in that window, a
// UDP-level probe against the endpoint (probeUDPReachable) tries to tell
// environment-level unreachability -- this test runner's network can't reach
// the advertised NodePort, e.g. outbound UDP is filtered in this CI network
// -- apart from an endpoint that answered UDP but never became the active
// transport, which points at a real bug (a broken handshake, or the
// transport stuck on fallback) rather than an environment limitation.
//
// The UDP probe is inherently ambiguous short of a full QUIC handshake:
// silently-dropped UDP and "nothing there" look identical from here. So this
// only skips on silence and fails on any other signal.
func (s *quicTunnelSuite) awaitQuicOrSkip(ctx context.Context) *itest.StatusResponse {
	rq := s.Require()

	// A plain rq.Eventually here would record a hard failure the moment the
	// deadline passes, before we get a chance to tell a skip apart from a
	// failure -- so this polls by hand instead.
	deadline := time.Now().Add(10 * time.Second)
	var last *itest.StatusResponse
	for {
		if st, err := itest.TelepresenceStatus(ctx); err == nil {
			last = st
			if st.RootDaemon != nil && strings.HasPrefix(st.RootDaemon.TunnelTransport, "quic ") {
				return st
			}
		}
		if time.Now().After(deadline) {
			break
		}
		time.Sleep(time.Second)
	}

	observed := ""
	if last != nil && last.RootDaemon != nil {
		observed = last.RootDaemon.TunnelTransport
	}
	if reachable, _ := probeUDPReachable(ctx, s.endpoint, 3*time.Second); !reachable {
		s.T().Skipf("QUIC endpoint %s did not elicit any UDP response from this test runner "+
			"(observed tunnel_transport: %q) -- skipping, likely a network that filters outbound "+
			"UDP to NodePorts", s.endpoint, observed)
	}
	rq.Failf("QUIC endpoint reachable but tunnel_transport never switched to quic",
		"endpoint %s answered a UDP probe, but status reported tunnel_transport %q", s.endpoint, observed)
	return last
}

// probeUDPReachable is a Go-native, protocol-agnostic analogue of `nc -zu`:
// it sends one UDP datagram to addr and waits briefly for any signal that
// something is there -- either response bytes or an error surfaced from a
// bubbled-up ICMP destination-unreachable. It does not attempt a QUIC
// handshake; it only distinguishes "got some signal" from "total silence",
// which is the most a bare UDP probe can reliably say.
func probeUDPReachable(ctx context.Context, addr string, timeout time.Duration) (reachable bool, err error) {
	d := net.Dialer{Timeout: timeout}
	conn, derr := d.DialContext(ctx, "udp", addr)
	if derr != nil {
		return false, derr
	}
	defer conn.Close()

	if _, werr := conn.Write([]byte("telepresence-quic-reachability-probe")); werr != nil {
		// A synchronous write error (e.g. an already-bubbled-up ICMP
		// unreachable) is itself a signal that the network path is open.
		return true, nil
	}

	_ = conn.SetReadDeadline(time.Now().Add(timeout))
	buf := make([]byte, 512)
	_, rerr := conn.Read(buf)
	if rerr == nil {
		return true, nil
	}
	var netErr net.Error
	if errors.As(rerr, &netErr) && netErr.Timeout() {
		return false, nil
	}
	// Any other read error (e.g. connection refused) is a signal.
	return true, nil
}

// requireQuicTransport asserts that "telepresence status" currently reports
// the quic transport with this suite's endpoint, waiting briefly for it to
// settle. Unlike awaitQuicOrSkip (used once, at connect, when reachability is
// still unproven), a failure here is never treated as a skip: by the time
// this is called, SetupSuite has already established that the endpoint is
// reachable and working.
func (s *quicTunnelSuite) requireQuicTransport(ctx context.Context) *itest.StatusResponse {
	rq := s.Require()
	want := "quic (" + s.endpoint + ")"
	var last *itest.StatusResponse
	rq.Eventually(func() bool {
		st, err := itest.TelepresenceStatus(ctx)
		if err != nil {
			return false
		}
		last = st
		return st.RootDaemon != nil && st.RootDaemon.TunnelTransport == want
	}, 10*time.Second, time.Second, "status never reported tunnel_transport %q", want)
	return last
}

// Test_VPNOnlyTransport verifies that plain VPN-only traffic to a
// non-intercepted service (no traffic-agent involved at all) works while the
// manager-bound tunnel is on the quic transport, and that status reports the
// expected endpoint.
func (s *quicTunnelSuite) Test_VPNOnlyTransport() {
	ctx := s.Context()
	rq := s.Require()

	pods := itest.RunningPods(ctx, "echo-easy", s.AppNamespace())
	rq.NotEmpty(pods, "expected at least one running echo-easy pod")

	rq.Eventually(func() bool {
		so, err := itest.Output(ctx, "curl", "--silent", "--max-time", "2", "echo-easy")
		return err == nil && strings.Contains(so, "Request served by")
	}, 30*time.Second, 2*time.Second, "echo-easy was not reachable through the VPN-only tunnel")

	s.requireQuicTransport(ctx)
}

// Test_TrafficAgentCoexistence verifies that a regular (sidecar) intercept
// keeps working the same way it always has while the manager-bound tunnel is
// on the quic transport (client-to-agent flows stay on port-forward per the
// design doc; only the manager-bound tunnel -- DNS, non-intercepted traffic
// -- moves to quic), and that status still reports quic once the intercept
// is active.
func (s *quicTunnelSuite) Test_TrafficAgentCoexistence() {
	ctx := s.Context()
	const svc = "echo-easy"

	port, cancel := itest.StartLocalHttpEchoServer(ctx, svc)
	defer cancel()

	itest.TelepresenceOk(ctx, "intercept", "--mount", "false", "--port", strconv.Itoa(port), svc)
	mustLeave := true
	defer func() {
		if mustLeave {
			itest.TelepresenceOk(ctx, "detach", svc)
		}
	}()

	itest.PingInterceptedEchoServer(ctx, svc, "80")
	s.requireQuicTransport(ctx)

	itest.TelepresenceOk(ctx, "detach", svc)
	mustLeave = false
}

// Test_AgentPortForwardDisabledRelaysOverQuic verifies that with
// cluster.agentPortForward=false (VPN-only mode: no direct client-to-agent
// port-forwards, and intercepts are unavailable by design), traffic to a
// workload that has a traffic-agent sidecar injected is forced to relay
// through the manager -- and therefore over the quic transport, since that's
// what's currently serving the manager-bound tunnel -- and still works. The
// sidecar is injected up front, on the suite's regular connection, by a
// short-lived intercept that is detached again before the VPN-only
// reconnect, so the test is independent of suite execution order.
func (s *quicTunnelSuite) Test_AgentPortForwardDisabledRelaysOverQuic() {
	const svc = "echo-easy"
	ctx := s.Context()

	// Inject the traffic-agent sidecar while intercepts are still available.
	port, cancel := itest.StartLocalHttpEchoServer(ctx, svc)
	defer cancel()
	itest.TelepresenceOk(ctx, "intercept", "--mount", "false", "--port", strconv.Itoa(port), svc)
	itest.TelepresenceOk(ctx, "detach", svc)

	cfgCtx := itest.WithConfig(ctx, func(cfg client.Config) {
		cfg.Cluster().AgentPortForward = false
	})
	s.TelepresenceConnect(cfgCtx)
	defer func() {
		itest.TelepresenceQuitOk(cfgCtx)
		// Restore the suite's shared connection for the remaining tests.
		s.TelepresenceConnect(s.Context())
	}()

	s.requireQuicTransport(cfgCtx)

	// The sidecar is still in the pod; without agent port-forwards the only
	// path to it is the manager relay, so this round-trip proves agent-bound
	// traffic works over the quic transport.
	s.Require().Eventually(func() bool {
		so, err := itest.Output(cfgCtx, "curl", "--silent", "--max-time", "2", svc)
		return err == nil && strings.Contains(so, "Request served by")
	}, 30*time.Second, 2*time.Second, "agent-injected %s was not reachable through the VPN-only tunnel", svc)
	s.requireQuicTransport(cfgCtx)
}

// Test_NodeAgentTransport verifies that a node-agent attach works normally
// while the manager-bound tunnel is on the quic transport. It reuses
// nodeAgentBase.assertNodeAgentIntercept for the attach and traffic
// round-trip instead of duplicating nodeAgentSuite's full matrix -- this
// suite only needs to know that node-agent mode and the quic transport don't
// interfere with each other.
func (s *quicTunnelSuite) Test_NodeAgentTransport() {
	ctx := s.Context()
	s.skipUnlessNodeAgentSupported()
	s.skipUnlessNodeAgentPodSecurityOK(ctx)

	// A dedicated workload: other tests in this suite inject a sidecar agent
	// into echo-easy, and node-agent mode refuses a workload that has one.
	const svc = "echo-one"
	s.ApplyApp(ctx, svc, "deploy/"+svc)
	defer s.DeleteSvcAndWorkload(ctx, "deploy", svc)

	s.assertNodeAgentIntercept(svc)
	s.requireQuicTransport(ctx)
}
