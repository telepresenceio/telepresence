package integration_test

import (
	"context"
	"errors"
	"fmt"
	"net"
	"slices"
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
	//
	// The QUIC dial happens once per connect with a short budget (quicDialTimeout,
	// 3s). A cold handshake -- or a forwarder that has only just received its
	// backend allowlist -- can miss it, leaving the session on grpc for good with
	// no mid-session re-probe. Since a fresh connect is a fresh dial, reconnect a
	// few times before concluding the endpoint is unusable, so a cold-start miss
	// isn't mistaken for an unreachable endpoint (which would wrongly skip the
	// whole suite).
	var last *itest.StatusResponse
	for attempt := 0; attempt < 4; attempt++ {
		deadline := time.Now().Add(10 * time.Second)
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
		itest.TelepresenceQuitOk(ctx)
		s.TelepresenceConnect(ctx)
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
// the quic transport with this suite's endpoint, waiting briefly (10s) for it
// to settle. Unlike awaitQuicOrSkip (used once, at connect, when reachability
// is still unproven), a failure here is never treated as a skip: by the time
// this is called, SetupSuite has already established that the endpoint is
// reachable and working.
func (s *quicTunnelSuite) requireQuicTransport(ctx context.Context) *itest.StatusResponse {
	return s.requireQuicTransportWithin(ctx, 10*time.Second)
}

// requireQuicTransportWithin is requireQuicTransport with a caller-supplied
// timeout, for callers (e.g. Test_ZManagerOutageAttachmentSurvival) that need a
// wider window than the default 10s -- e.g. because the client's manager
// connection has to run its own reconnect cycle first.
func (s *quicTunnelSuite) requireQuicTransportWithin(ctx context.Context, timeout time.Duration) *itest.StatusResponse {
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
	}, timeout, time.Second, "status never reported tunnel_transport %q", want)
	return last
}

// quicDiscoverableEndpoints returns "<address>:<quicNodePort>" for every cluster
// Node's preferred address -- ExternalIP if it has one, else InternalIP -- exactly the
// rule cmd/traffic/cmd/manager/quictunnel.Discovery applies for a NodePort Service.
// Test_ZZDiscoveryNodePort doesn't know, or need to know, which Node the client's
// candidate probe will settle on; it only needs the full set of endpoints that would
// be a legitimate discovery result to check the observed one against. Every kind node
// hits the InternalIP fallback, since kind assigns no ExternalIP.
func (s *quicTunnelSuite) quicDiscoverableEndpoints(ctx context.Context) []string {
	out, err := itest.KubectlOut(ctx, "", "get", "nodes", "-o",
		`jsonpath={range .items[*]}{.status.addresses[?(@.type=="ExternalIP")].address}{"|"}{.status.addresses[?(@.type=="InternalIP")].address}{"\n"}{end}`)
	s.Require().NoError(err, "failed to list node addresses")

	var endpoints []string
	for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		if line == "" {
			continue
		}
		externalIPs, internalIPs, _ := strings.Cut(line, "|")
		host := ""
		if fs := strings.Fields(externalIPs); len(fs) > 0 {
			host = fs[0]
		} else if fs := strings.Fields(internalIPs); len(fs) > 0 {
			host = fs[0]
		}
		if host != "" {
			endpoints = append(endpoints, net.JoinHostPort(host, strconv.Itoa(quicNodePort)))
		}
	}
	s.Require().NotEmpty(endpoints, "no cluster node reported a usable address")
	return endpoints
}

// Test_ZZDiscoveryNodePort proves the zero-configuration path this phase exists for:
// with quicTunnel.externalHost/externalPort left unset, the traffic-manager discovers
// the NodePort Service's assigned port and the cluster's Node addresses itself, and
// the client reaches the endpoint it derives with no further configuration. Runs last
// in the suite (its name sorts after Test_ZManagerOutageAttachmentSurvival): it
// reconfigures the traffic-manager away from the explicit-override install every other
// test in this suite relies on, and there is no reason for a later test to see it.
//
// helm upgrade with -f/--set fully replaces the settings it names (this chart is never
// installed with --reuse-values, see itest.TelepresenceHelmInstall), so simply omitting
// externalHost/externalPort here is enough to revert them to the chart defaults (""/0)
// and put the manager back into discovery mode; quicTunnel.service.nodePort stays
// pinned to quicNodePort so the Service itself, and therefore every candidate's port,
// doesn't change.
func (s *quicTunnelSuite) Test_ZZDiscoveryNodePort() {
	ctx := s.Context()
	rq := s.Require()

	// The harness always installs the traffic-manager namespace-scoped (a static
	// namespaces list -- see itest.TelepresenceHelmInstall -- selects the
	// namespace-scoped Roles in trafficManagerRbac/namespace-scope.yaml), which is
	// precisely the shape whose NodePort discovery degrades to nothing: no Node read
	// access, by design. A first version of this test stopped there, proving only the
	// degradation. To exercise discovery itself, grant this traffic-manager exactly
	// the Node access a cluster-scoped install's ClusterRole carries, before the
	// upgrade below rolls the manager pod (the manager checks its Node access once,
	// at startup).
	managerNs := s.ManagerNamespace()
	roleName := "quic-discovery-nodes-" + managerNs
	rq.NoError(itest.Kubectl(ctx, "", "create", "clusterrole", roleName,
		"--verb=get,list,watch", "--resource=nodes"))
	defer func() { _ = itest.Kubectl(ctx, "", "delete", "clusterrole", roleName) }()
	rq.NoError(itest.Kubectl(ctx, "", "create", "clusterrolebinding", roleName,
		"--clusterrole="+roleName, "--serviceaccount="+managerNs+":traffic-manager"))
	defer func() { _ = itest.Kubectl(ctx, "", "delete", "clusterrolebinding", roleName) }()

	s.TelepresenceHelmInstallOK(ctx, true,
		"--set", "nodeAgent.enabled=true",
		"--set", "quicTunnel.enabled=true",
		"--set", "quicTunnel.service.type=NodePort",
		"--set", fmt.Sprintf("quicTunnel.service.nodePort=%d", quicNodePort),
	)

	// A fresh connect is required: the endpoint descriptor (and the candidate list it
	// carries) is fetched once at connect, and the live session's copy was fetched
	// under the old, override-based configuration. Reconnect until the fresh session
	// lands on quic rather than asserting after a single reconnect, for the same
	// reason Test_ZManagerOutageAttachmentSurvival does: the upgrade rolled the
	// traffic-manager pod, the forwarder needs a few seconds to relearn the new pod's
	// IP for its backend allowlist, and a client that connects before that refresh
	// falls back to grpc for that whole session (there is no mid-session re-probe).
	endpoints := s.quicDiscoverableEndpoints(ctx)
	want := make([]string, len(endpoints))
	for i, ep := range endpoints {
		want[i] = "quic (" + ep + ")"
	}
	rq.Eventually(func() bool {
		itest.TelepresenceQuitOk(ctx)
		s.TelepresenceConnect(ctx)
		st, err := itest.TelepresenceStatus(ctx)
		return err == nil && st.RootDaemon != nil && slices.Contains(want, st.RootDaemon.TunnelTransport)
	}, 90*time.Second, 15*time.Second,
		"status never reported tunnel_transport as one of %q", want)
}

// requireAgentTransport asserts that "telepresence status" currently reports
// the given transport ("quic" or "grpc" -- see the transportQUIC/
// transportPortForward constants in pkg/client/agentpf/quic.go) for the live
// connection to workload's traffic-agent, waiting briefly for it to settle.
// The agent_transports list (root_daemon.agent_transports) is populated only
// while an agent is actually connected -- see toStatusAgentTransports in
// pkg/client/cli/cmd/status.go -- so this only makes sense to call while an
// attachment (intercept or ingest) to workload is live.
func (s *quicTunnelSuite) requireAgentTransport(ctx context.Context, workload, want string) *itest.StatusResponse {
	return s.requireAgentTransportWithin(ctx, workload, want, 30*time.Second)
}

// requireAgentTransportWithin is requireAgentTransport with a caller-supplied
// timeout, for callers that need a wider window -- e.g. after a manager CA
// rotation, when a surviving agent must reconnect and re-fetch its server
// certificate before the client can reach it over quic again.
func (s *quicTunnelSuite) requireAgentTransportWithin(ctx context.Context, workload, want string, timeout time.Duration) *itest.StatusResponse {
	rq := s.Require()
	var last *itest.StatusResponse
	rq.Eventually(func() bool {
		st, err := itest.TelepresenceStatus(ctx)
		if err != nil {
			return false
		}
		last = st
		if st.RootDaemon == nil {
			return false
		}
		for _, at := range st.RootDaemon.AgentTransports {
			if at.Workload == workload {
				return at.Transport == want
			}
		}
		return false
	}, timeout, time.Second,
		"status never reported agent transport %q for workload %q", want, workload)
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
// keeps working while the manager-bound tunnel is on the quic transport, that
// status still reports quic once the intercept is active, and -- now that
// agents run their own QUIC listeners behind the forwarder (phase 6, "Agent
// connections over QUIC" in docs/plans/quic-transport/design.md) -- that the
// client-to-agent attachment itself has also come up over quic rather than
// falling back to its per-agent port-forward.
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
	s.requireAgentTransport(ctx, svc, "quic")

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
// interfere with each other. It also asserts, while the node-agent
// attachment is still live, that the client-to-agent connection itself rides
// quic: node-agent Jobs get the same AGENT_QUIC_PORT plumbing as an injected
// sidecar (see pkg/agentmap/generator.go), so there is nothing
// node-agent-specific about the agent transport upgrade.
func (s *quicTunnelSuite) Test_NodeAgentTransport() {
	ctx := s.Context()
	s.skipUnlessNodeAgentSupported()
	s.skipUnlessNodeAgentPodSecurityOK(ctx)

	// A dedicated workload: other tests in this suite inject a sidecar agent
	// into echo-easy, and node-agent mode refuses a workload that has one.
	const svc = "echo-one"
	s.ApplyApp(ctx, svc, "deploy/"+svc)
	defer s.DeleteSvcAndWorkload(ctx, "deploy", svc)

	s.assertNodeAgentIntercept(svc, []string{"--node-agent"}, func() {
		s.requireAgentTransport(ctx, svc, "quic")
	})
	s.requireQuicTransport(ctx)
}

// quicForwarderLabelSelector selects the quic-forwarder Deployment's pods
// (charts/telepresence-oss/templates/quicforwarder.yaml, built from the
// "telepresence.quicForwarderSelectorLabels" chart helper).
const quicForwarderLabelSelector = "app=quic-forwarder,telepresence=quic-forwarder"

// Test_ForwarderRestartSurvival exercises the forwarder's central failure-mode
// claim from "The forwarder" section of docs/plans/quic-transport/design.md:
// killing the stateless packet router must not force a permanent fallback to
// the gRPC transport. With the connection already established and the
// manager-bound tunnel on quic, it deletes every quic-forwarder pod, waits
// for the Deployment to report a ready replacement, and then asserts that
// traffic recovers -- and that "telepresence status" still reports the quic
// transport throughout, never grpc.
//
// Either the client's QUIC connection survives the restart outright (CID
// routing plus path validation to the replacement pod's new address) or, at
// worst, traffic stalls until the client's 15s keep-alives establish a fresh
// flow through the new pod (kube-proxy's UDP conntrack can keep pinning the
// old flow to the now-gone pod IP for a while). Either way tunnel_transport
// must never flip to grpc: that would mean the client gave up on quic
// instead of riding out the forwarder restart, which is exactly what a
// stateless forwarder is supposed to make unnecessary.
func (s *quicTunnelSuite) Test_ForwarderRestartSurvival() {
	ctx := s.Context()
	rq := s.Require()

	// Establish the baseline: connected, transport already on quic.
	s.requireQuicTransport(ctx)

	// The chart deploys quic-forwarder in the same namespace as the
	// traffic-manager (templates/quicforwarder.yaml uses
	// traffic-manager.namespace), not the app namespace.
	managerNs := s.ManagerNamespace()
	rq.NoError(itest.Kubectl(ctx, managerNs, "delete", "pod", "-l", quicForwarderLabelSelector),
		"failed to delete quic-forwarder pod(s)")

	rq.NoError(itest.RolloutStatusWait(ctx, managerNs, "deploy/quic-forwarder"),
		"quic-forwarder Deployment did not report a ready replacement after pod deletion")

	// 60s ceiling: kube-proxy's UDP conntrack may keep routing the client's
	// existing flow to the deleted pod's address for a while; the client's
	// 15s keep-alives are what eventually punch a fresh flow through to the
	// replacement pod. The transport assertion stays strict throughout --
	// this only widens the window for traffic (and status) to catch up, it
	// never tolerates an observed "grpc" transport as a passing state.
	want := "quic (" + s.endpoint + ")"
	rq.Eventually(func() bool {
		so, err := itest.Output(ctx, "curl", "--silent", "--max-time", "2", "echo-easy")
		if err != nil || !strings.Contains(so, "Request served by") {
			return false
		}
		st, err := itest.TelepresenceStatus(ctx)
		return err == nil && st.RootDaemon != nil && st.RootDaemon.TunnelTransport == want
	}, 60*time.Second, 2*time.Second,
		"tunnel did not recover over the quic transport after the forwarder restarted "+
			"(it must not have permanently fallen back to grpc)")

	// A final, non-Eventually round-trip: recovery isn't just a momentarily
	// true poll result, plain traffic through the tunnel keeps working.
	so, err := itest.Output(ctx, "curl", "--silent", "--max-time", "5", "echo-easy")
	rq.NoError(err, "curl through the tunnel failed after forwarder recovery")
	rq.Contains(so, "Request served by")
}

// Test_ZManagerOutageAttachmentSurvival is the decisive test for the
// forwarder architecture described in "The forwarder" section of
// docs/plans/quic-transport/design.md: "The manager dying no longer affects
// client<->agent traffic at all: the forwarder routes packets and the agents
// terminate their own TLS, so attachments keep flowing through a manager
// restart exactly as they do today." With a sidecar intercept active and its
// agent connection already confirmed on quic, it scales the traffic-manager
// Deployment to zero, confirms the intercepted round-trip keeps working while
// the manager is entirely gone, then scales the manager back up and confirms
// both the manager-bound tunnel and the agent attachment recover.
//
// The client session's own connection to the manager is expected to error
// out and reconnect around this outage (session keepalives, watches, etc.);
// that is not what this test is about and is not asserted one way or the
// other. What must hold throughout is the client<->agent data path for
// cluster-originated traffic: an in-cluster request to the intercepted
// workload is tunneled agent -> forwarder -> laptop handler, which never
// touches the manager. (Requests the developer makes from the laptop through
// the VPN are a different path: they need the manager tunnel for cluster DNS
// and subnet routing, so those are not expected to survive and are not what
// this asserts.)
func (s *quicTunnelSuite) Test_ZManagerOutageAttachmentSurvival() {
	ctx := s.Context()
	rq := s.Require()
	const svc = "echo-easy"

	port, cancel := itest.StartLocalHttpEchoServer(ctx, svc)
	defer cancel()

	itest.TelepresenceOk(ctx, "intercept", "--mount", "false", "--port", strconv.Itoa(port), svc)
	mustLeave := true
	defer func() {
		if mustLeave {
			// Retrying, not itest.TelepresenceOk: see detachRetrying's doc for why a
			// plain one-shot detach right after this test's outage is brittle.
			s.detachRetrying(ctx, svc)
		}
	}()

	// Baseline: intercepted traffic works and the agent attachment is
	// already on quic before the outage starts.
	itest.PingInterceptedEchoServer(ctx, svc, "80")
	s.requireAgentTransport(ctx, svc, "quic")

	managerNs := s.ManagerNamespace()
	rq.NoError(itest.Kubectl(ctx, managerNs, "scale", "deploy/traffic-manager", "--replicas", "0"),
		"failed to scale the traffic-manager Deployment to zero")

	// Wait for the manager pod to actually be gone, not just for the scale
	// command to have been accepted -- the outage assertions below are only
	// meaningful once there is no traffic-manager pod left to (accidentally)
	// service the intercepted traffic.
	rq.Eventually(func() bool {
		return len(itest.RunningPods(ctx, "traffic-manager", managerNs)) == 0
	}, 60*time.Second, 2*time.Second, "traffic-manager pod did not terminate after scaling to zero")

	restoreManager := true
	defer func() {
		if restoreManager {
			_ = itest.Kubectl(ctx, managerNs, "scale", "deploy/traffic-manager", "--replicas", "1")
			_ = itest.RolloutStatusWait(ctx, managerNs, "deploy/traffic-manager")
		}
	}()

	// The decisive assertion: a request that ORIGINATES IN THE CLUSTER and hits
	// the intercepted workload still reaches the client's local handler with no
	// traffic-manager pod at all. This is deliberately not PingInterceptedEchoServer
	// (as the baseline above uses): that pings the service through the client's VPN,
	// whose DNS resolution and subnet routing run over the manager-bound tunnel and so
	// legitimately cannot work while the manager is gone. The property the forwarder
	// architecture actually provides is that the client<->agent data path --
	// cluster-originated traffic tunneled from the agent to the laptop handler -- is
	// independent of the manager. So drive it from an in-cluster pod (curlimages/curl,
	// same pattern as not_connected_test.go). Polled a handful of times rather than
	// once, so a single lucky round-trip can't mask a race with the outage taking hold.
	for i := 0; i < 3; i++ {
		out, err := s.KubectlOut(ctx, "run", "-i", fmt.Sprintf("quic-outage-probe-%d", i),
			"--rm", "--image", "curlimages/curl", "--restart", "Never", "--command", "--",
			"curl", "--silent", "--max-time", "5", "http://"+svc)
		rq.NoError(err, "in-cluster request to intercepted %s failed while the manager was down", svc)
		rq.Contains(out, svc+" from intercept at /",
			"in-cluster request to intercepted %s did not reach the local handler while the manager was down", svc)
		if i < 2 {
			time.Sleep(2 * time.Second)
		}
	}

	rq.NoError(itest.Kubectl(ctx, managerNs, "scale", "deploy/traffic-manager", "--replicas", "1"),
		"failed to scale the traffic-manager Deployment back to one")
	restoreManager = false
	rq.NoError(itest.RolloutStatusWait(ctx, managerNs, "deploy/traffic-manager"),
		"traffic-manager Deployment did not report ready after scaling back up")

	// Recovery, same session: no quit/reconnect. The client's manager-bound tunnel
	// fell back to grpc while the manager was gone (the forwarder's backend allowlist
	// had no live pod left to route the old QUIC connection to, and the new manager
	// process's CA no longer verifies the session's client certificate anyway); the
	// client's re-probe loop retries the QUIC dial on an interval, re-fetching the
	// endpoint descriptor (fresh CA, fresh session-scoped client certificate) on every
	// attempt, so it recovers once the new manager process is reachable and the
	// forwarder has relearned its pod IP -- without the client ever knowing a
	// restart happened. Bound the wait comfortably above the re-probe interval to
	// also absorb the forwarder's own relearning delay.
	s.requireQuicTransportWithin(ctx, 150*time.Second)

	// The client<->agent attachment's own QUIC connection is independent of the
	// manager (see the decisive assertion above) and is not expected to have tripped
	// at all during the outage; confirm it is still serving quic once the
	// manager-bound tunnel has also recovered, now that the re-probe's recovery also
	// resets any per-agent QUIC state a fallback would have left behind.
	s.requireAgentTransport(ctx, svc, "quic")
}

// detachRetrying detaches from workload, retrying briefly on failure. The rootd
// session's QUIC re-probe (what requireQuicTransportWithin above just waited on) is a
// separate connection from the userd connector daemon's own control-plane connection
// to the manager, which detach itself needs; that connection resolves the manager pod
// by listing pods with a live selector (pkg/client/portforward/resolve.go
// ResolveSvcToPod) each time it (re)dials, rather than watching for changes, so right
// after a manager pod replacement it can lag a few seconds behind both
// RolloutStatusWait (which only waits on the Deployment's own rollout status) and the
// QUIC transport's own recovery. A plain one-shot detach attempted in that window
// fails with "no running pods with accessible ports found for service"; retrying
// absorbs it without weakening what the test actually asserts (that already happened
// above).
func (s *quicTunnelSuite) detachRetrying(ctx context.Context, workload string) {
	rq := s.Require()
	rq.Eventually(func() bool {
		_, _, err := itest.Telepresence(ctx, "detach", workload)
		return err == nil
	}, 30*time.Second, 2*time.Second, "detach %q kept failing", workload)
}
