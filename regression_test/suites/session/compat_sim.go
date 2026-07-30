package session

import (
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/cli"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/managers"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/workloads"
)

// compatSimVersion is the manager version CompatSim's managers.Compat spec
// installs. It has to sit in a narrow window:
//
//   - >= 2.21.0: pkg/client/userd/trafficmgr/session.go's connectMgr refuses
//     any older manager outright ("traffic manager version %s is too old"),
//     so a lower value would fail before ever reaching the RPCs this suite
//     means to exercise.
//   - < 2.26.0: the oldest ring of checkCompat's widened gates
//     (cmd/traffic/cmd/manager/service.go's WatchAgentPodsDelta/
//     WatchAgentsDelta/WatchInterceptsDelta) only report Unimplemented
//     below that version.
//
// 2.21.0, the floor itself, also trips every later gate
// (WatchAgentPodsInNamespacesDelta at 2.28.0, WatchSessionEvents and the
// QUIC RPCs at 2.31.0), so it is the version that forces every widened
// fallback chain at once while still leaving GetKnownWorkloadKinds (2.20.0)
// and WatchWorkloads (2.21.0-alpha.4) working normally.
const compatSimVersion = "2.21.0"

// CompatSim proves the client's Unimplemented fallback chains work end to
// end against a manager that predates every RPC checkCompat's widening
// added, without needing an actual old traffic-manager image:
// managers.Compat sets compatibility.version, which makes a current
// manager return Unimplemented for RPCs newer than compatSimVersion (see
// checkCompat in cmd/traffic/cmd/manager/service.go). That forces the
// client down its oldest paths: WatchSessionEvents falls back to the
// legacy WatchAgentsDelta/WatchInterceptsDelta watchers
// (sessionevents.go), which in turn fall back to WatchAgents/
// WatchIntercepts (agents.go, intercept.go), and the agent-pod watcher
// falls back from WatchAgentPodsInNamespacesDelta through
// WatchAgentPodsDelta to the full-snapshot WatchAgentPods
// (agentpf/watch.go).
type CompatSim struct {
	rt.Suite
}

func init() {
	rt.Register(&CompatSim{}, rt.InArea("session"), rt.NeedsManager(managers.Compat(compatSimVersion)))
}

// Test_ListAndIntercept connects against the emulated-old manager, confirms
// list still reports the workload, then runs a full intercept round trip
// (attach, routed to the local handler, detach, routed back to the
// cluster): the fallback chains above have to have delivered a correct
// agent-pod and intercept view for any of this to work.
func (s *CompatSim) Test_ListAndIntercept() {
	t := s.T()
	conn := s.Connect()
	wl := s.Workload(workloads.Echo("compat-sim"))
	ls := s.LocalEcho()

	s.True(listContains(conn.List(t), wl.Name, wl.Namespace), "list should show %s.%s", wl.Name, wl.Namespace)

	a := conn.Intercept(t, wl, rt.ToLocal(ls, "http"), cli.MountFalse())
	rt.RoutedToLocal(t, wl.ServiceURL(), ls)

	a.Detach(t)
	rt.RoutedToCluster(t, wl.ServiceURL())
}

// listContains reports whether entries contains a workload named name in ns.
func listContains(entries []cli.ListEntry, name, ns string) bool {
	for _, e := range entries {
		if e.Name == name && e.Namespace == ns {
			return true
		}
	}
	return false
}
