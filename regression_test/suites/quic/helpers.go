package quic

import (
	"context"
	"fmt"
	"net/http"
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
// transport is active ("quic (<endpoint>)", quic_test.go's
// requireQuicTransport/awaitQuicOrSkip): the endpoint varies with the
// cluster (self-discovered by the manager, see managers.QuicNodePort's doc
// comment), so this area matches the prefix rather than a specific
// endpoint.
const quicPrefix = "quic "

// grpcFallback is the exact tunnel_transport value status reports once an
// already-established quic connection has tripped and fallen back
// mid-session (quic_test.go's Test_AZForwarderOutageFallsBackAndRecovers,
// line ~655): distinct from the plain "grpc" a session that never
// completed a quic dial in the first place reports (quic_test.go's
// Test_ZYUnreachableEndpointFallsBack, line ~423: "an initial dial that
// fails quietly is not a fallback event").
const grpcFallback = "grpc (fallback)"

const (
	quicPollInterval = 2 * time.Second
	// quicDialSettle is double the opportunistic quic dial's own budget
	// (quicDialTimeout, pkg/client/rootd/quic.go), the margin quic_test.go's
	// Test_ZYUnreachableEndpointFallsBack itself waits before treating an
	// observed transport as settled rather than a probe still in flight.
	quicDialSettle = 6 * time.Second
	// quicStatusTimeout bounds a plain (non-reconnecting) status poll.
	quicStatusTimeout = 30 * time.Second
	// quicDiscoveryTimeout bounds the reconnect loop in awaitTransportPrefix:
	// quic_test.go's reconnectUntilQuic uses the same 90s ceiling.
	quicDiscoveryTimeout = 90 * time.Second
	// quicForwarderTermTimeout bounds the wait for the quic-forwarder's
	// pod(s) to actually terminate after scaling to zero, matching
	// quic_test.go's Test_AZForwarderOutageFallsBackAndRecovers.
	quicForwarderTermTimeout = 60 * time.Second
	// quicFallbackTimeout/quicRecoveryTimeout bound the outage suite's two
	// post-scale polls, matching quic_test.go's own 150s ceilings for the
	// same transitions.
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
// root_daemon.agent_transports entry for workload reports transport want, or
// fails t after timeout. The agent_transports list is populated only while
// an attachment to workload is live (pkg/client/cli/cmd/status.go's
// toStatusAgentTransports), mirroring quic_test.go's requireAgentTransport.
func awaitAgentTransport(t testing.TB, ctx context.Context, tp *cli.TP, workload, want string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		st := fetchStatus(t, ctx, tp)
		for _, at := range st.RootDaemon.AgentTransports {
			if at.Workload == workload && at.Transport == want {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("agent transport for workload %q never reported %q", workload, want)
		}
		time.Sleep(quicPollInterval)
	}
}

// awaitTransportPrefix reconnects (quit + connect) to ns until status
// reports a root_daemon.tunnel_transport beginning with prefix, or fails t.
// Used only before any attachment exists: reconnecting quits the daemon,
// which would drop a live intercept. Mirrors quic_test.go's
// reconnectUntilQuic -- the opportunistic quic dial runs once per connect
// and never retries mid-session after an initial miss, and a freshly rolled
// manager pod's forwarder needs a moment to relearn its IP for its backend
// allowlist.
func awaitTransportPrefix(
	t testing.TB, ctx context.Context, tp *cli.TP, ns string, conn *rt.Conn, prefix string,
) *rt.Conn {
	t.Helper()
	deadline := time.Now().Add(quicDiscoveryTimeout)
	for {
		time.Sleep(quicDialSettle)
		st := fetchStatus(t, ctx, tp)
		if strings.HasPrefix(st.RootDaemon.TunnelTransport, prefix) {
			return conn
		}
		if time.Now().After(deadline) {
			t.Fatalf("root_daemon.tunnel_transport never began with %q (last: %q)",
				prefix, st.RootDaemon.TunnelTransport)
		}
		conn.Disconnect(t)
		conn = rt.Reconnect(t, ctx, ns)
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
// grpcFallback, or fails t after timeout. Mirrors quic_test.go's combined
// poll in Test_AZForwarderOutageFallsBackAndRecovers: in the zombie window
// before the dead quic connection's idle timeout expires, tunnel streams
// open locally and hang, so the probe fails -- this rides that out -- and
// each attempt is also the stream open that, once the connection has died,
// trips the provider into fallback.
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
