package compat

import (
	"sort"
	"testing"

	"github.com/telepresenceio/telepresence/rpc/v2/manager"
)

// exemption is one manager.Manager RPC method the compat-core test set
// (manifest.go) does not claim, with a one-line reason it doesn't need to.
type exemption struct {
	method string
	reason string
}

// exemptions is the checked-in list of manager.Manager RPC methods no
// compat-core test claims, each with why that's fine. TestManifestCoversServiceDesc
// fails if any method in manager.Manager_ServiceDesc is neither claimed in
// manifest.go nor listed here, and fails if any entry here names a method
// that isn't actually in the ServiceDesc (a stale or mistyped exemption).
//
//nolint:gochecknoglobals // static, checked-in compat table
var exemptions = []exemption{
	// Agent-only: called exclusively from cmd/traffic/cmd/agent, never from
	// any pkg/client caller. There is no Mechanism* method in the current
	// ServiceDesc to exempt alongside these.
	{"ReviewIntercept", "agent-only: called only from cmd/traffic/cmd/agent/client.go, deciding whether a filtered intercept matches; the client never calls it directly (HeaderFilter/Test_Header exercises it end-to-end via the real agent, see manifest.go)"},
	{"ReportMetrics", "agent-only: called only from cmd/traffic/cmd/agent (server.go, fwd/http.go, fwd/tcp.go, fwdstate.go); no client caller"},
	{"WatchLogLevel", "agent-only: called only from cmd/traffic/cmd/agent/client.go to receive log-level pushes; no client caller"},
	{"GetQuicAgentCert", "agent-only: called only from cmd/traffic/cmd/agent/quic.go; no client caller"},
	{"ReconnectAgent", "agent-only: called only from cmd/traffic/cmd/agent/client.go; the client-side equivalent is ReconnectClient"},
	{"ArriveAsAgent", "agent-only: called only from cmd/traffic/cmd/agent/client.go; the client-side equivalent is ArriveAsClient"},

	// quicforwarder-only.
	{"WatchQuicBackends", "quicforwarder-only: called only from cmd/traffic/cmd/quicforwarder/allowlist.go"},

	// QUIC tunnel: genuinely client-invoked, but only when the QUIC tunnel
	// is enabled, which no compat-core test does yet.
	{"GetQuicTunnelEndpoint", "client-invoked only when the QUIC tunnel is enabled (pkg/client/rootd/quic.go, pkg/client/agentpf/quic.go); exempt until a compat-core QUIC cell lands (see m4-spec section 1's note on version-gated features)"},

	// Namespace watching: client-invoked on every session whose mapped
	// namespace set is empty, but only against a manager >= 2.32.0 (the
	// managerSupportsWatchNamespaces gate); older managers get the client's
	// own Kubernetes namespace watcher instead, so no compat-core cell
	// fails without it.
	{"WatchNamespaces", "client-invoked for the watch-all-namespaces case (pkg/client/userd/trafficmgr/session.go's updateClientConfig), gated on manager >= 2.32.0 with a client-side namespace watcher as the older-manager path; no compat-core assertion depends on which watcher ran"},

	// Session credential: client-invoked lazily, and every failure path
	// (Unimplemented from an older manager, any fetch error) degrades to a
	// credential-less mount or agent call, so no compat-core cell fails
	// without it.
	{"GetSessionCredential", "client-invoked lazily on first mount/agent connection (pkg/client/sessioncred, pkg/client/userd/trafficmgr/session_credential.go, pkg/client/rootd/session_credential.go); degrades to credential-less operation on any error, so no compat-core cell claims it yet"},

	// Legacy/dead: the manager implements these, but no client anywhere in
	// the repo (pkg/client, cmd/traffic/cmd/agent) calls them.
	{"GetTelepresenceAPI", "dead: implemented by cmd/traffic/cmd/manager/service.go, but no caller anywhere in pkg/client or cmd/traffic/cmd/agent"},
	{"GetIntercept", "dead from the client's perspective: the CLI's own GetIntercept (pkg/client/cli/cmd/detach.go etc.) calls the CONNECTOR's GetIntercept, which the userd daemon serves from its cached session state (pkg/client/userd/daemon/grpc.go:533) rather than proxying to manager.Manager.GetIntercept"},
	{"GetKnownWorkloadKinds", "scaffolded end-to-end (connector RPC, manager RPC, checkCompat gate at 2.20.0) but no CLI command invokes the connector's GetKnownWorkloadKinds yet; not wired into any client flow to claim"},

	// Superseded by the combined WatchSessionEvents stream (claimed by
	// attach.AttachModes and session.WorkloadWatch): against the built
	// manager, sessionevents.go's watchSessionEvents wins every time, and
	// only falls back to these three watcher families -- each itself a
	// primary/legacy pair -- on Unimplemented from a manager older than
	// 2.31.0 (WatchSessionEvents' checkCompat gate). No compat-core test
	// targets a manager that old.
	{"WatchAgentsDelta", "superseded by WatchSessionEvents: reached only via watchSessionEventsLegacy's fallback against a pre-2.31.0 manager (pkg/client/userd/trafficmgr/sessionevents.go, agents.go)"},
	{"WatchAgents", "superseded by WatchAgentsDelta in turn: reached only when WatchAgentsDelta itself is Unimplemented (pkg/client/userd/trafficmgr/agents.go's watchAgentsLoop)"},
	{"WatchInterceptsDelta", "superseded by WatchSessionEvents: reached only via watchSessionEventsLegacy's fallback against a pre-2.31.0 manager (pkg/client/userd/trafficmgr/sessionevents.go, intercept.go)"},
	{"WatchIntercepts", "superseded by WatchInterceptsDelta in turn: reached only when WatchInterceptsDelta itself is Unimplemented (pkg/client/userd/trafficmgr/intercept.go)"},
	{"WatchAgentPodsInNamespacesDelta", "superseded by WatchSessionEvents: reached only via watchSessionEventsLegacy's fallback against a pre-2.31.0 manager (pkg/client/agentpf/watch.go's WatchPods, the top tier of its own three-way fallback)"},
	{"WatchAgentPodsDelta", "superseded by WatchAgentPodsInNamespacesDelta in turn: reached only when that RPC itself is Unimplemented (pkg/client/agentpf/watch.go)"},
	{"WatchAgentPods", "superseded by WatchAgentPodsDelta in turn (also used, independently, by rootd's own agent-pod relay watcher): reached only when the delta tiers above are Unimplemented (pkg/client/agentpf/watch.go, pkg/client/rootd/session.go, pkg/client/userd/trafficmgr/podrelay.go)"},

	// LookupDNS: real (not legacy), but not the RPC A/AAAA resolution uses
	// against a current manager.
	{"LookupDNS", "superseded, for A/AAAA queries, by Lookup: pkg/client/rootd/session.go's clusterLookup only falls back to LookupDNS (complexClusterLookup) for non-A/AAAA query types, or when the manager is older than 2.25.0 (the client-side lookupSequencer gate); every compat-core test's DNS traffic is A/AAAA against a manager >= 2.25.0, claimed instead via Lookup (intercept.InterceptRouting)"},

	// Genuinely client-invoked, but not by any compat-core-labeled test.
	{"SetLogLevel", "client-invoked only via `telepresence loglevel` (pkg/client/cli/cmd/loglevel.go); no compat-core test runs that command"},
	{"ReleaseAgent", "client-invoked only by the ingest verb's release path (pkg/client/userd/trafficmgr/ingest.go); AttachMatrix's ingest/deployment cell isn't compat-core labeled (only AttachModes' intercept/deployment cell is, per m4-spec section 2's scope for this milestone)"},
	{"UninstallAgents", "client-invoked only via `telepresence uninstall --agent`/`--all-agents` (pkg/client/userd/trafficmgr/session.go); no compat-core test runs that command"},
}

// TestManifestCoversServiceDesc walks manager.Manager_ServiceDesc (both
// unary Methods and streaming Streams) and fails when a method is neither
// claimed by manifest nor listed in exemptions, or when an exemptions entry
// names a method the ServiceDesc doesn't actually have (a stale or
// mistyped exemption -- the check m4-spec section 3 calls for). It also
// flags a manifest.go claim that doesn't correspond to a real RPC, and any
// method claimed AND exempted at once, both of which would silently hide a
// bug in the table above.
func TestManifestCoversServiceDesc(t *testing.T) {
	svcMethods := serviceDescMethods()

	claimed := map[string]string{} // method -> the first testName claiming it
	for testName, methods := range manifest {
		for _, m := range methods {
			if _, ok := svcMethods[m]; !ok {
				t.Errorf("manifest: %q claims %q, which is not a method of manager.Manager_ServiceDesc", testName, m)
				continue
			}
			if other, dup := claimed[m]; dup {
				t.Logf("manifest: %q is claimed by both %q and %q (harmless, but redundant)", m, other, testName)
				continue
			}
			claimed[m] = testName
		}
	}

	exempt := map[string]string{} // method -> reason
	for _, ex := range exemptions {
		if _, ok := svcMethods[ex.method]; !ok {
			t.Errorf("exemptions: %q is not a method of manager.Manager_ServiceDesc (stale or mistyped entry)", ex.method)
			continue
		}
		if _, dup := exempt[ex.method]; dup {
			t.Errorf("exemptions: %q is listed more than once", ex.method)
			continue
		}
		if ex.reason == "" {
			t.Errorf("exemptions: %q has no reason", ex.method)
		}
		exempt[ex.method] = ex.reason
		if testName, isClaimed := claimed[ex.method]; isClaimed {
			t.Errorf("%q is both claimed (by %q) and exempted (%q): pick one", ex.method, testName, ex.reason)
		}
	}

	var uncovered []string
	for m := range svcMethods {
		_, isClaimed := claimed[m]
		_, isExempt := exempt[m]
		if !isClaimed && !isExempt {
			uncovered = append(uncovered, m)
		}
	}
	if len(uncovered) > 0 {
		sort.Strings(uncovered)
		t.Errorf("manager.Manager RPC methods neither claimed by a compat-core test (manifest.go) nor exempted "+
			"(manifest_test.go's exemptions): %v", uncovered)
	}
}

// serviceDescMethods returns the set of every RPC method name (unary and
// streaming) manager.Manager_ServiceDesc actually declares, derived at
// runtime rather than hand-copied: the guard this whole package exists for
// would be worthless if it drifted from the real ServiceDesc.
func serviceDescMethods() map[string]struct{} {
	out := make(map[string]struct{}, len(manager.Manager_ServiceDesc.Methods)+len(manager.Manager_ServiceDesc.Streams))
	for _, m := range manager.Manager_ServiceDesc.Methods {
		out[m.MethodName] = struct{}{}
	}
	for _, s := range manager.Manager_ServiceDesc.Streams {
		out[s.StreamName] = struct{}{}
	}
	return out
}
