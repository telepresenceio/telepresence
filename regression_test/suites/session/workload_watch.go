package session

import (
	"context"
	"encoding/json"
	"os/exec"
	"sync"
	"time"

	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/cli"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/managers"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/workloads"
)

// workloadEventTimeout/workloadEventInterval bound the poll for a
// WatchWorkloads event to arrive. listStreamTimeout bounds the wait for
// `list --format json-stream` to report a newly created workload.
const (
	workloadEventTimeout  = 90 * time.Second
	workloadEventInterval = 2 * time.Second
	listStreamTimeout     = 45 * time.Second
)

// WorkloadWatch proves the manager's WatchWorkloads RPC and the CLI's `list
// --format json-stream` both surface a workload's lifecycle (added, agent
// installed, intercepted) as it happens. Ported from workload_watch_test.go's
// essentials, with batching/ordering tolerated: Eventually polls collected
// event state rather than asserting a fixed Recv sequence.
type WorkloadWatch struct {
	rt.Suite
}

func init() {
	rt.Register(&WorkloadWatch{}, rt.InArea("session"), rt.NeedsManager(managers.Default))
}

// workloadEvents collects, thread-safely, the essential lifecycle booleans
// this suite cares about for one (name, namespace) workload, from a manager
// WatchWorkloads stream. Events for every other workload -- every other one
// already or later present in the shared app namespace -- are ignored.
type workloadEvents struct {
	name, namespace string

	mu             sync.Mutex
	added          bool
	agentInstalled bool
	intercepted    bool
}

// run reads delta events from stream until it errors (typically because the
// caller canceled the context the stream was opened with) or ends.
func (w *workloadEvents) run(stream manager.Manager_WatchWorkloadsClient) {
	for {
		delta, err := stream.Recv()
		if err != nil {
			return
		}
		for _, ev := range delta.GetEvents() {
			wi := ev.GetWorkload()
			if wi.GetName() != w.name || wi.GetNamespace() != w.namespace {
				continue
			}
			w.mu.Lock()
			switch ev.GetType() { //nolint:exhaustive // DELETED needs no bookkeeping here
			case manager.WorkloadEvent_ADDED_UNSPECIFIED, manager.WorkloadEvent_MODIFIED:
				w.added = true
				switch wi.GetAgentState() { //nolint:exhaustive // NO_AGENT_UNSPECIFIED needs no bookkeeping here
				case manager.WorkloadInfo_INSTALLED:
					w.agentInstalled = true
				case manager.WorkloadInfo_INTERCEPTED:
					w.agentInstalled = true
					w.intercepted = true
				}
			}
			w.mu.Unlock()
		}
	}
}

func (w *workloadEvents) sawAdded() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.added
}

func (w *workloadEvents) sawAgentInstalled() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.agentInstalled
}

func (w *workloadEvents) sawIntercepted() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.intercepted
}

// Test_ManagerWatchSeesLifecycle establishes its own manager session
// (arriveAsClient: WatchWorkloads needs one, and reusing the CLI's own would
// require reading its session-info cache file for no benefit, since the
// manager tracks workload state per namespace, not per watching session),
// applies a fresh workload, and drives an intercept through the real CLI
// connection while polling the collected events for added/agent-installed/
// intercepted.
func (s *WorkloadWatch) Test_ManagerWatchSeesLifecycle() {
	t := s.T()
	ctx := s.Ctx()
	ns := s.AppNamespace()
	conn := s.Connect()

	env := rt.Env{Ctx: ctx, T: t, R: s.R()}
	mc, closeMC, err := rt.ManagerClient(env, managers.ManagerNamespace)
	s.Require().NoError(err)
	defer closeMC()

	watchCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	sessionInfo := arriveAsClient(t, watchCtx, mc, "rtest-workloadwatch", ns)

	stream, err := mc.WatchWorkloads(watchCtx, &manager.WorkloadEventsRequest{SessionInfo: sessionInfo})
	s.Require().NoError(err)

	wl := s.Workload(workloads.Echo("workload-watch-target"))
	events := &workloadEvents{name: wl.Name, namespace: wl.Namespace}
	go events.run(stream)

	s.Eventually(events.sawAdded, workloadEventTimeout, workloadEventInterval,
		"no added/modified WatchWorkloads event for %s", wl.Name)

	ls := s.LocalEcho()
	a := conn.Intercept(t, wl, rt.ToLocal(ls, "http"), cli.MountFalse())

	s.Eventually(events.sawAgentInstalled, workloadEventTimeout, workloadEventInterval,
		"no agent-installed WatchWorkloads event for %s", wl.Name)
	s.Eventually(events.sawIntercepted, workloadEventTimeout, workloadEventInterval,
		"no intercepted WatchWorkloads event for %s", wl.Name)

	a.Detach(t)
}

// Test_ListJSONStreamSeesNewWorkload proves `telepresence list --format
// json-stream` (not `--output json-stream`: that deprecated flag wraps
// output in a {cmd,stdout,stderr,err} envelope instead of streaming clean
// snapshots -- see pkg/client/cli/output/output.go's package doc and
// pkg/client/cli/global's FlagFormat/FlagOutput) emits a snapshot containing
// a workload created after the stream started. cli.TP.Run only returns after
// the process exits, so this drives the subprocess directly (TP's Exe/Env/
// Dir are exported for exactly this) and decodes the concatenated JSON array
// values --format json-stream writes with no separator between them.
func (s *WorkloadWatch) Test_ListJSONStreamSeesNewWorkload() {
	t := s.T()
	ctx := s.Ctx()
	ns := s.AppNamespace()
	s.Connect()

	tp := s.CLI()
	streamCtx, cancel := context.WithCancel(ctx)
	cmd := exec.CommandContext(streamCtx, tp.Exe, "list", "--format", "json-stream")
	cmd.Env = tp.Env
	cmd.Dir = tp.Dir
	stdout, err := cmd.StdoutPipe()
	s.Require().NoError(err)
	s.Require().NoError(cmd.Start())
	// Deferred in this order so unwinding (including via t.Fatalf's
	// runtime.Goexit in the timeout branch below) always cancels first: Wait
	// would otherwise block forever on a still-running, uncanceled process.
	defer func() { _ = cmd.Wait() }()
	defer cancel()

	found := make(chan struct{})
	go func() {
		dec := json.NewDecoder(stdout)
		for {
			var snap []cli.ListEntry
			if err := dec.Decode(&snap); err != nil {
				return
			}
			for _, e := range snap {
				if e.Name == "list-stream-target" && e.Namespace == ns {
					close(found)
					return
				}
			}
		}
	}()

	// Applying the workload blocks on its rollout: real, unhurried
	// concurrent work. By the time it returns, the background process has
	// long since started and sent at least one snapshot -- no sleep needed
	// to "wait for the stream to be ready".
	s.Workload(workloads.Echo("list-stream-target"))

	select {
	case <-found:
	case <-time.After(listStreamTimeout):
		t.Fatalf("list --format json-stream did not report the new workload within %s", listStreamTimeout)
	}
}
