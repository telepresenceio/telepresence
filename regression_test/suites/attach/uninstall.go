package attach

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/telepresenceio/telepresence/v2/regression_test/framework/cli"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/managers"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/workloads"
)

// uninstallTimeout/uninstallPollInterval bound the wait for a workload's
// agent to disappear from `list --agents` after `telepresence uninstall`:
// the RPC evicts the workload's pods synchronously, but the replacement pod
// still needs a few seconds to schedule and settle, the same rollout
// waitRollout waits out elsewhere in this package.
const (
	uninstallTimeout      = 120 * time.Second
	uninstallPollInterval = 2 * time.Second
)

// Uninstall proves the `telepresence uninstall <workload>` verb, the only
// integration coverage for it anywhere: the CLI verb drives the manager's
// agent-uninstall path (session.Uninstall in
// pkg/client/userd/trafficmgr/session.go calls the manager's
// UninstallAgents RPC, handled by State.UninstallAgents in
// cmd/traffic/cmd/manager/state/state.go, which evicts the workload's pods
// and drops its agent config). `helm uninstall`'s pre-delete hook scrubs
// every agent through the same pod-eviction machinery, only reached via the
// agent-injector's /uninstall endpoint instead
// (cmd/traffic/cmd/manager/mutator/agent_injector.go's Uninstall).
type Uninstall struct {
	rt.Suite
}

func init() {
	rt.Register(&Uninstall{}, rt.InArea("attach"), rt.NeedsManager(managers.Default))
}

// Test_UninstallRemovesAgent intercepts and detaches a workload of its own
// (detach never uninstalls the agent, only the attachment), confirms the
// agent is still there, then runs `telepresence uninstall <workload>` and
// confirms the agent disappears from `list --agents`.
func (s *Uninstall) Test_UninstallRemovesAgent() {
	t := s.T()
	conn := s.Connect()
	wl := s.Workload(workloads.Echo("attach-uninstall"))
	ls := s.LocalEcho()

	a := conn.Intercept(t, wl, rt.ToLocal(ls, "http"), cli.MountFalse())
	a.Detach(t)

	s.True(hasInstalledAgent(&s.Suite, wl), "list --agents should still show %s.%s after detach", wl.Name, wl.Namespace)

	_, stderr, err := s.CLI().Run(s.Ctx(), "uninstall", wl.Name)
	s.Require().NoError(err, "uninstall should succeed: %s", stderr)

	s.Require().Eventually(func() bool {
		return !hasInstalledAgent(&s.Suite, wl)
	}, uninstallTimeout, uninstallPollInterval,
		"list --agents should no longer show %s.%s after uninstall", wl.Name, wl.Namespace)
}

// Test_UninstallAllAgentsRemovesSeveralWorkloads gives three two-replica
// workloads an agent each, then runs `telepresence uninstall --all-agents`.
// That RPC path removes every agent in the connected namespace in one
// unpaced pass, the same function the Helm pre-delete hook calls, and all
// six pods must lose their traffic-agent container. The namespace is
// private so that the namespace-wide uninstall touches no other suite's
// workloads; the manager has to restart to manage it, which also kills the
// default connection's port-forward, so that connection is freed first.
func (s *Uninstall) Test_UninstallAllAgentsRemovesSeveralWorkloads() {
	t := s.T()
	ctx := s.Ctx()
	r := s.R()
	s.Manager()
	env := rt.Env{Ctx: ctx, T: t, R: r}
	ns := rt.PrivateNamespace(env, "uninstall-all")
	rt.Mutate(t, rt.ConnectionFixture(s.AppNamespace())).Disconnect(t)
	s.Require().NoError(rt.RestartManager(env))
	conn := rt.Mutate(t, rt.ConnectionFixture(ns))
	ls := s.LocalEcho()

	wls := make([]*rt.Workload, 3)
	for i := range wls {
		wl := rt.Get(t, rt.WorkloadFixture(ns, workloads.EchoReplicas(fmt.Sprintf("uninstall-all-%d", i), 2)))
		a := conn.Intercept(t, wl, rt.ToLocal(ls, "http"), cli.MountFalse())
		a.Detach(t)
		wls[i] = wl
	}
	for _, wl := range wls {
		s.Require().Eventually(func() bool {
			return agentContainers(ctx, r, ns, wl.Name) == 2
		}, uninstallTimeout, uninstallPollInterval, "both pods of %s should carry a traffic-agent", wl.Name)
	}

	_, stderr, err := s.CLI().Run(ctx, "uninstall", "--all-agents")
	s.Require().NoError(err, "uninstall --all-agents should succeed: %s", stderr)

	s.Require().Eventually(func() bool {
		for _, wl := range wls {
			if agentContainers(ctx, r, ns, wl.Name) != 0 || hasInstalledAgent(&s.Suite, wl) {
				return false
			}
		}
		return true
	}, uninstallTimeout, uninstallPollInterval, "no pod in %s should carry a traffic-agent after uninstall --all-agents", ns)
}

// agentContainers counts the traffic-agent containers across the pods
// matching the app=name selector in ns.
func agentContainers(ctx context.Context, r *rt.Runtime, ns, name string) int {
	out, err := r.Kubectl(ctx, ns, "get", "pod", "-l", "app="+name, "-o",
		"jsonpath={.items[*].spec.containers[*].name}")
	if err != nil {
		return -1
	}
	count := 0
	for _, f := range strings.Fields(out) {
		if f == "traffic-agent" {
			count++
		}
	}
	return count
}

// hasInstalledAgent reports whether `list --agents` includes wl: that
// filter (rpc.ListRequest_INSTALLED_AGENTS) keeps only workloads the
// manager's agent snapshot still tracks, regardless of any current
// attachment (pkg/client/userd/trafficmgr/session.go's sMap lookup).
// Package-level rather than a method on Uninstall, so any suite in this
// package can reuse it after its own `uninstall` call.
func hasInstalledAgent(s *rt.Suite, wl *rt.Workload) bool {
	var entries []cli.ListEntry
	if err := s.CLI().JSON(s.Ctx(), &entries, "list", "--agents", "--format", "json"); err != nil {
		s.T().Fatalf("list --agents: %v", err)
	}
	return listContains(entries, wl.Name, wl.Namespace)
}
