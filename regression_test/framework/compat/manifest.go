package compat

// manifest maps a compat-core test (area.Suite/Test_Method) to the
// manager.Manager RPC methods it is understood to exercise against a real
// traffic-manager. manifest_test.go walks manager.Manager_ServiceDesc
// (rpc/manager) and fails when a method is neither claimed here nor listed
// in manifest_test.go's exemption table: the guard that keeps the
// compat-core test set (rt.CompatCore) honest about RPC coverage.
//
// Every claim below was verified by reading the actual client call chain
// the test drives against the CURRENT (built) traffic-manager -- not
// assumed from the RPC's name -- so it reflects what the compat-core suite
// really exercises today. Where a test's own comment names the RPC
// explicitly (e.g. session/manager_info.go's WatchClusterInfo note), this
// map follows that.
//
//nolint:gochecknoglobals // static, checked-in compat table
var manifest = map[string][]string{
	// Every Connect drives connectMgr (pkg/client/userd/trafficmgr/
	// session.go): ArriveAsClient establishes the session, Remain renews it
	// while the daemon lives, Depart ends it on quit. Test_Status's `status`
	// call additionally drives UpdateStatus -> updateClientConfig ->
	// GetClientConfig (pkg/client/userd/trafficmgr/session.go:956).
	"smoke.SmokeConnected/Test_Status": {
		"ArriveAsClient",
		"Remain",
		"Depart",
		"GetClientConfig",
	},
	// `version --format json` resolves the manager version via the
	// connect-time Version call cached on the session (pkg/client/k8s/
	// connect.go's getVersion) and the agent image FQN via
	// trafficAgentFQN -> userD.AgentImageFQN -> manager.GetAgentImageFQN
	// (pkg/client/cli/cmd/version.go).
	"smoke.SmokeConnected/Test_Version": {
		"Version",
		"GetAgentImageFQN",
	},
	// The intercept/deployment cell drives: GetAgentConfig (the intercept
	// command reads the workload's sidecar config, pkg/client/cli/intercept/
	// command.go:414), EnsureAgent + PrepareIntercept + CreateIntercept
	// (trafficmgr/intercept.go's createIntercept), RemoveIntercept (on
	// detach), and WatchSessionEvents (the live session's combined
	// agent-pod/intercept delta stream, pkg/client/userd/trafficmgr/
	// sessionevents.go, which is what "agent+intercept watches" resolves to
	// against a manager new enough to offer it).
	"attach.AttachModes/Test_Attach": {
		"GetAgentConfig",
		"EnsureAgent",
		"PrepareIntercept",
		"CreateIntercept",
		"RemoveIntercept",
		"WatchSessionEvents",
	},
	// A header-filtered intercept's real HTTP traffic round-trips over the
	// manager's Tunnel RPC (pkg/client/rootd/stream_creator.go's
	// streamCreator; also the transport shape rootd/quic.go and agentpf/
	// clients.go share). ReviewIntercept (manager -> agent, deciding whether
	// the filter matches) is exercised end-to-end by the same request, but
	// is agent-only from the client's side -- see manifest_test.go's
	// exemption.
	"intercept.HeaderFilter/Test_Header": {
		"Tunnel",
	},
	// `gather-logs` drives GetLogs (pkg/client/userd/trafficmgr/
	// gather_logs.go) for both the manager and the intercepted workload's
	// agent.
	"session.GatherLogs/Test_Matrix": {
		"GetLogs",
	},
	// Test_ManagerWatchSeesLifecycle opens its own WatchWorkloads stream
	// directly (rt.ManagerClient), and the intercept it drives through the
	// real CLI connection runs the live session's WatchSessionEvents.
	"session.WorkloadWatch/Test_ManagerWatchSeesLifecycle": {
		"WatchWorkloads",
		"WatchSessionEvents",
	},
	// Calls WatchClusterInfo directly (rt.ManagerClient); see the test's own
	// comment on why WatchClusterInfo, not a nonexistent GetClusterInfo.
	"session.ManagerInfo/Test_ServiceSubnetMatchesStatus": {
		"WatchClusterInfo",
	},
	// rootd's persistent WatchClusterInfo watch (pkg/client/rootd/
	// session.go) runs for the life of every connection, including the
	// disconnect/reconnect cycle this test drives. ReconnectClient is the
	// RPC a live session's WatchSessionEvents stream calls on every
	// reconnect (pkg/client/userd/trafficmgr/session.go's reconnectManager,
	// invoked from sessionevents.go's onReset) -- exercised here rather
	// than in ConnectReconnect (framework/rt.Sudo-gated, excluded from
	// compat-core).
	"connect.ConnectLifecycle/Test_Lifecycle": {
		"ReconnectClient",
		"WatchClusterInfo",
	},
	// A/AAAA resolution of wl.ServiceURL() -- what every request in this
	// test sends -- goes through the root daemon's simple lookup path
	// (pkg/client/rootd/session.go's simpleLookup), which is manager.Lookup,
	// not manager.LookupDNS (see manifest_test.go's exemption note on the
	// two RPCs' actual roles).
	"intercept.InterceptRouting/Test_MultiReplica": {
		"Lookup",
	},
}
