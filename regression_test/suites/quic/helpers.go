package quic

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/telepresenceio/telepresence/v2/regression_test/framework/cli"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/managers"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
)

// quicStatus and rootDaemonStatus extend regression_test/framework/cli.Status
// with the root-daemon transport fields this area asserts on
// (pkg/client/cli/cmd/status.go's RootDaemonStatus.TunnelTransport/
// AgentTransports): "telepresence status --format json"'s
// root_daemon.tunnel_transport/agent_transports. cli.Status doesn't carry
// them -- no other area needs them -- so this area extends locally with its
// own JSON-tagged mirror instead of changing the shared framework type.
type quicStatus struct {
	RootDaemon rootDaemonStatus `json:"root_daemon"`
}

type rootDaemonStatus struct {
	TunnelTransport string           `json:"tunnel_transport,omitempty"`
	AgentTransports []agentTransport `json:"agent_transports,omitempty"`
}

type agentTransport struct {
	Workload  string `json:"workload,omitempty"`
	Transport string `json:"transport,omitempty"`
}

// quicPrefix begins the tunnel_transport value status reports once the quic
// transport is active ("quic (<endpoint>)"): the endpoint varies with the
// cluster (self-discovered by the manager, see managers.QuicNodePort's doc
// comment), so this area matches the prefix rather than a specific
// endpoint.
const quicPrefix = "quic "

// grpcFallback is the exact tunnel_transport value status reports once an
// already-established quic connection has tripped and fallen back
// mid-session: distinct from the plain "grpc" a session that never
// completed a quic dial in the first place reports -- an initial dial that
// fails quietly is not a fallback event.
const grpcFallback = "grpc (fallback)"

const (
	quicPollInterval = 2 * time.Second
	// quicDialSettle is double the opportunistic quic dial's own budget
	// (quicDialTimeout, pkg/client/rootd/quic.go): the margin this area
	// waits before treating an observed transport as settled rather than a
	// probe still in flight.
	quicDialSettle = 6 * time.Second
	// quicStatusTimeout bounds a plain (non-reconnecting) status poll.
	quicStatusTimeout = 30 * time.Second
	// quicAgentTransportTimeout bounds the wait for an agent to report the
	// quic transport. It is longer than quicStatusTimeout because it waits
	// on more than a status refresh: the agent has just been injected into
	// a freshly rolled workload and dials its own quic connection, which on
	// a loaded single-node cluster outlasts the plain status budget.
	quicAgentTransportTimeout = 90 * time.Second
	// quicDiscoveryTimeout bounds the reconnect loop in awaitTransportPrefix.
	quicDiscoveryTimeout = 90 * time.Second
	// quicForwarderTermTimeout bounds the wait for the quic-forwarder's
	// pod(s) to actually terminate after scaling to zero.
	quicForwarderTermTimeout = 60 * time.Second
	// quicFallbackTimeout/quicRecoveryTimeout bound the outage suite's two
	// post-scale polls for the same transitions. quicRecoveryTimeout is also
	// reused by ManagerOutage's post-recovery poll, which needs the same
	// 150s budget for the same reason: the client re-fetches a fresh
	// endpoint descriptor and re-probes quic on its own interval after the
	// manager pod is replaced.
	quicFallbackTimeout = 150 * time.Second
	quicRecoveryTimeout = 150 * time.Second
)

// fetchStatus runs `telepresence status --format json` and unmarshals it
// into quicStatus.
func fetchStatus(t testing.TB, ctx context.Context, tp *cli.TP) *quicStatus {
	t.Helper()
	var st quicStatus
	if err := tp.JSON(ctx, &st, "status", "--format", "json"); err != nil {
		t.Fatalf("status: %v", err)
	}
	return &st
}

// pollStatus polls status until match(tunnel_transport) is true, or fails t
// after timeout naming desc.
func pollStatus(
	t testing.TB, ctx context.Context, tp *cli.TP, timeout time.Duration,
	match func(transport string) bool, desc string,
) *quicStatus {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		st := fetchStatus(t, ctx, tp)
		if match(st.RootDaemon.TunnelTransport) {
			return st
		}
		if time.Now().After(deadline) {
			t.Fatalf("root_daemon.tunnel_transport never %s (last: %q)", desc, st.RootDaemon.TunnelTransport)
		}
		time.Sleep(quicPollInterval)
	}
}

// awaitStatusTransport polls status (no reconnect) until
// root_daemon.tunnel_transport equals want, or fails t after timeout.
func awaitStatusTransport(
	t testing.TB, ctx context.Context, tp *cli.TP, want string, timeout time.Duration,
) *quicStatus {
	t.Helper()
	return pollStatus(t, ctx, tp, timeout,
		func(tt string) bool { return tt == want }, fmt.Sprintf("reported %q", want))
}

// awaitStatusTransportPrefix polls status (no reconnect) until
// root_daemon.tunnel_transport begins with prefix, or fails t after timeout.
// Used mid-session, when a reconnect would drop a live attachment: the
// client's background re-probe (armed by a fallback trip) recovers the
// transport on its own interval, with no reconnect needed.
func awaitStatusTransportPrefix(
	t testing.TB, ctx context.Context, tp *cli.TP, prefix string, timeout time.Duration,
) *quicStatus {
	t.Helper()
	return pollStatus(t, ctx, tp, timeout,
		func(tt string) bool { return strings.HasPrefix(tt, prefix) }, fmt.Sprintf("began with %q", prefix))
}

// awaitAgentTransport polls status (no reconnect) until the
// root_daemon.agent_transports entry for workload reports the "quic"
// transport, or fails t after quicAgentTransportTimeout. The
// agent_transports list is populated only while an attachment to workload
// is live (pkg/client/cli/cmd/status.go's toStatusAgentTransports). Every
// caller in this area waits for the quic agent transport specifically, so
// the transport is not a parameter, like awaitTransportPrefix's quicPrefix.
//
// The failure names the transport the agent actually settled on: "never
// reported quic" alone cannot distinguish an agent still dialing from one
// that fell back to grpc for the whole session.
func awaitAgentTransport(t testing.TB, ctx context.Context, tp *cli.TP, workload string) {
	t.Helper()
	deadline := time.Now().Add(quicAgentTransportTimeout)
	for {
		observed := ""
		st := fetchStatus(t, ctx, tp)
		for _, at := range st.RootDaemon.AgentTransports {
			if at.Workload != workload {
				continue
			}
			if at.Transport == "quic" {
				return
			}
			observed = at.Transport
		}
		if time.Now().After(deadline) {
			if observed == "" {
				t.Fatalf("workload %q never appeared in agent_transports", workload)
			}
			t.Fatalf("agent transport for workload %q settled on %q, never %q", workload, observed, "quic")
		}
		time.Sleep(quicPollInterval)
	}
}

// awaitTransportPrefix reconnects (quit + connect) to ns until status
// reports a root_daemon.tunnel_transport beginning with quicPrefix, or
// fails t. Used only before any attachment exists: reconnecting quits the
// daemon, which would drop a live intercept. The opportunistic quic dial
// runs once per connect and never retries mid-session after an initial
// miss, and a freshly rolled manager pod's forwarder needs a moment to
// relearn its IP for its backend allowlist. Every caller in this area is
// establishing the quic transport specifically, so the prefix is
// quicPrefix rather than a parameter. opts carry connection flags (e.g.
// Datagrams' --mapped-namespaces isolation) into every reconnect attempt,
// not just the caller's first connect.
func awaitTransportPrefix(
	t testing.TB, ctx context.Context, tp *cli.TP, ns string, conn *rt.Conn, opts ...rt.ConnOpt,
) *rt.Conn {
	t.Helper()
	deadline := time.Now().Add(quicDiscoveryTimeout)
	for {
		time.Sleep(quicDialSettle)
		st := fetchStatus(t, ctx, tp)
		if strings.HasPrefix(st.RootDaemon.TunnelTransport, quicPrefix) {
			return conn
		}
		if time.Now().After(deadline) {
			t.Fatalf("root_daemon.tunnel_transport never began with %q (last: %q)",
				quicPrefix, st.RootDaemon.TunnelTransport)
		}
		conn.Disconnect(t)
		conn = rt.Reconnect(t, ctx, ns, opts...)
	}
}

// freshConnect frees whatever connection to ns is currently memoized (if
// any) and reconnects via rt.Reconnect, so the resulting session was
// established after the suite's declared manager spec (already guaranteed
// live by Suite.SetupTest) was live, rather than possibly adopting a
// session dialed under a different quic suite's spec. Every quic suite uses
// this instead of Suite.Connect(): this area churns the shared release's
// quicTunnel configuration across suites more than any other area, and
// RunArea only guarantees suites sharing a manager-spec hash run adjacent to
// each other, not a fixed order relative to suites on a different spec
// (Disabled's Default vs. the others' quic-nodeport/quic-unreachable).
// Mirrors injector/helpers.go's switchManagerSpec, minus the explicit
// ManagerFixture switch it also does (Suite.SetupTest already covers that
// here).
func freshConnect(t testing.TB, ctx context.Context, ns string) *rt.Conn {
	t.Helper()
	conn := rt.Mutate(t, rt.ConnectionFixture(ns))
	conn.Disconnect(t)
	return rt.Reconnect(t, ctx, ns)
}

// probeCluster is a single, short-timeout GET against url on a fresh
// transport, reporting whether it reached the workload directly (status
// 200). Unlike rt.RoutedToCluster (which polls to success or fails t), this
// is a boolean probe: awaitFallbackWithTraffic below combines it with a
// status check in one poll loop.
func probeCluster(url string) bool {
	c := &http.Client{Timeout: 2 * time.Second, Transport: &http.Transport{DisableKeepAlives: true}}
	resp, err := c.Get(url)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// awaitFallbackWithTraffic polls url and status together until a plain
// in-cluster round trip succeeds AND status simultaneously reports
// grpcFallback, or fails t after timeout. In the zombie window before the
// dead quic connection's idle timeout expires, tunnel streams open locally
// and hang, so the probe fails -- this rides that out -- and each attempt
// is also the stream open that, once the connection has died, trips the
// provider into fallback.
func awaitFallbackWithTraffic(t testing.TB, ctx context.Context, tp *cli.TP, url string) {
	t.Helper()
	deadline := time.Now().Add(quicFallbackTimeout)
	for {
		if probeCluster(url) {
			st := fetchStatus(t, ctx, tp)
			if st.RootDaemon.TunnelTransport == grpcFallback {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("traffic and %q status never both held while the quic-forwarder was down", grpcFallback)
		}
		time.Sleep(quicPollInterval)
	}
}

// quicForwarderDeployment is the chart's quic-forwarder Deployment name
// (charts/telepresence-oss/templates/quicforwarder.yaml), installed in the
// traffic-manager's namespace.
const quicForwarderDeployment = "quic-forwarder"

// quicForwarderSelector selects the quic-forwarder Deployment's pods
// (charts/telepresence-oss/templates/_helpers.tpl's
// "telepresence.quicForwarderSelectorLabels" helper: app=quic-forwarder,
// telepresence=quic-forwarder).
const quicForwarderSelector = "app=quic-forwarder,telepresence=quic-forwarder"

// scaleQuicForwarderDown scales the quic-forwarder Deployment to zero
// replicas and waits for its pod(s) to actually terminate: `rollout status`
// alone would report success as soon as the scale is accepted, before the
// old pod is gone.
func scaleQuicForwarderDown(t testing.TB, ctx context.Context, r *rt.Runtime) {
	t.Helper()
	mgrNS := managers.ManagerNamespace
	if _, err := r.Kubectl(ctx, mgrNS, "scale", "deploy/"+quicForwarderDeployment, "--replicas", "0"); err != nil {
		t.Fatalf("scale %s to 0: %v", quicForwarderDeployment, err)
	}
	deadline := time.Now().Add(quicForwarderTermTimeout)
	for {
		out, err := r.Kubectl(ctx, mgrNS, "get", "pods", "-l", quicForwarderSelector, "-o", "name")
		if err == nil && strings.TrimSpace(out) == "" {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("quic-forwarder pod(s) did not terminate after scaling to 0")
		}
		time.Sleep(quicPollInterval)
	}
}

// scaleQuicForwarderUp scales the quic-forwarder Deployment back to one
// replica and waits for the rollout to report ready.
func scaleQuicForwarderUp(t testing.TB, ctx context.Context, r *rt.Runtime) {
	t.Helper()
	mgrNS := managers.ManagerNamespace
	if _, err := r.Kubectl(ctx, mgrNS, "scale", "deploy/"+quicForwarderDeployment, "--replicas", "1"); err != nil {
		t.Fatalf("scale %s to 1: %v", quicForwarderDeployment, err)
	}
	_, err := r.Kubectl(ctx, mgrNS, "rollout", "status", "deploy/"+quicForwarderDeployment, "--timeout=90s")
	if err != nil {
		t.Fatalf("rollout status %s: %v", quicForwarderDeployment, err)
	}
}

// deleteQuicForwarderPods deletes every quic-forwarder pod and waits for the
// Deployment to report a ready replacement. Unlike scaleQuicForwarderDown/Up
// (a planned outage, used by Outage), this simulates an unplanned pod loss
// that the Deployment's own controller repairs on its own.
func deleteQuicForwarderPods(t testing.TB, ctx context.Context, r *rt.Runtime) {
	t.Helper()
	mgrNS := managers.ManagerNamespace
	if _, err := r.Kubectl(ctx, mgrNS, "delete", "pod", "-l", quicForwarderSelector); err != nil {
		t.Fatalf("delete %s pod(s): %v", quicForwarderDeployment, err)
	}
	if _, err := r.Kubectl(ctx, mgrNS, "rollout", "status", "deploy/"+quicForwarderDeployment, "--timeout=90s"); err != nil {
		t.Fatalf("rollout status %s: %v", quicForwarderDeployment, err)
	}
}

// quicForwarderRestartTimeout bounds the wait for traffic to recover after
// deleting the quic-forwarder pod(s): kube-proxy's UDP conntrack can keep
// pinning the client's existing flow to the deleted pod's address for a
// while, and the client's 15s keep-alives are what eventually punch a fresh
// flow through to the replacement pod.
const quicForwarderRestartTimeout = 60 * time.Second

// awaitRecoveryNeverLeavingQuic polls a plain in-cluster round trip against
// url until it succeeds, failing t immediately (not merely timing out) the
// moment status ever reports a tunnel_transport that doesn't start with
// quicPrefix: the stateless-router property under test is that the
// transport rides out the forwarder's replacement without ever falling
// back, not just that it eventually recovers.
func awaitRecoveryNeverLeavingQuic(t testing.TB, ctx context.Context, tp *cli.TP, url string) {
	t.Helper()
	deadline := time.Now().Add(quicForwarderRestartTimeout)
	for {
		st := fetchStatus(t, ctx, tp)
		if !strings.HasPrefix(st.RootDaemon.TunnelTransport, quicPrefix) {
			t.Fatalf("tunnel_transport left %q during forwarder recovery (observed %q): the quic-forwarder "+
				"is supposed to be a stateless router the client rides out",
				quicPrefix, st.RootDaemon.TunnelTransport)
		}
		if probeCluster(url) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("traffic never recovered after the quic-forwarder pod(s) were deleted")
		}
		time.Sleep(quicPollInterval)
	}
}

// udpEchoDialTimeout/udpEchoDialInterval/udpEchoSettle/udpEchoReadTimeout
// bound udpEchoRoundTrip's dial retry, the post-dial settle (a UDP Dial
// succeeds immediately without confirming anything is listening yet), and
// the echoed-response read.
const (
	udpEchoDialTimeout  = 12 * time.Second
	udpEchoDialInterval = 3 * time.Second
	udpEchoSettle       = 2 * time.Second
	udpEchoReadTimeout  = 5 * time.Second
)

// udpEchoRoundTrip sends msg to wl's UDP-echo Service (workloads.UDPEcho)
// through the VPN and requires the echoed payload back, proving tunneled
// UDP traffic actually round-trips over whichever transport is currently
// active.
func udpEchoRoundTrip(t testing.TB, ctx context.Context, wl *rt.Workload, msg string) {
	t.Helper()
	addr := fmt.Sprintf("%s.%s:%d", wl.SvcName, wl.Namespace, wl.Port)

	var conn net.Conn
	var d net.Dialer
	deadline := time.Now().Add(udpEchoDialTimeout)
	for {
		var err error
		conn, err = d.DialContext(ctx, "udp", addr)
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("dial udp %s: %v", addr, err)
		}
		time.Sleep(udpEchoDialInterval)
	}
	defer conn.Close()

	// A UDP Dial succeeds immediately without confirming anything is
	// listening yet.
	time.Sleep(udpEchoSettle)

	if _, err := conn.Write([]byte(msg)); err != nil {
		t.Fatalf("write to %s: %v", addr, err)
	}
	if err := conn.SetReadDeadline(time.Now().Add(udpEchoReadTimeout)); err != nil {
		t.Fatalf("set read deadline on %s: %v", addr, err)
	}
	buf := make([]byte, 0x10000)
	n, err := conn.Read(buf)
	if err != nil && !errors.Is(err, io.EOF) {
		t.Fatalf("read from %s: %v", addr, err)
	}
	if !strings.Contains(string(buf[:n]), msg) {
		t.Fatalf("echoed payload %q from %s does not contain %q", string(buf[:n]), addr, msg)
	}
}

// datagramCountersLogRE extracts the "received" total from the manager's
// periodic datagram-counters log line (cmd/traffic/cmd/manager/
// quictunnel/listener.go's logDatagramStatsLoop, formatted by
// pkg/tunnel.DatagramCounters.String).
var datagramCountersLogRE = regexp.MustCompile(`datagram counters: sent \d+, received (\d+),`)

// quicDatagramCountersTimeout bounds the wait for the manager's periodic
// datagram-counters log line to report a nonzero received count.
const quicDatagramCountersTimeout = 45 * time.Second

// awaitNonzeroDatagramsReceived polls the traffic-manager Deployment's logs
// until a datagramCountersLogRE match reports a nonzero received count, or
// fails t after quicDatagramCountersTimeout.
func awaitNonzeroDatagramsReceived(t testing.TB, ctx context.Context, r *rt.Runtime) {
	t.Helper()
	mgrNS := managers.ManagerNamespace
	deadline := time.Now().Add(quicDatagramCountersTimeout)
	for {
		out, err := r.Kubectl(ctx, mgrNS, "logs", "deploy/"+trafficManagerDeployment)
		if err == nil {
			for _, m := range datagramCountersLogRE.FindAllStringSubmatch(out, -1) {
				if m[1] != "0" {
					return
				}
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("manager never logged a nonzero datagram received count")
		}
		time.Sleep(quicPollInterval)
	}
}
