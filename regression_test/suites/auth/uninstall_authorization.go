package auth

import (
	"context"
	"slices"
	"strings"

	"github.com/telepresenceio/telepresence/v2/regression_test/framework/cli"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/workloads"
)

// agentContainer is the traffic-agent's container name inside an injected pod.
const agentContainer = "traffic-agent"

// agentPodUIDs returns the UIDs of the live pods of the workload name in ns
// that carry a traffic-agent container.
func agentPodUIDs(ctx context.Context, r *rt.Runtime, ns, name string) []string {
	out, err := r.Kubectl(ctx, ns, "get", "pod", "-l", "app="+name, "-o",
		`jsonpath={range .items[*]}{.metadata.uid}|{.metadata.deletionTimestamp}|{.spec.containers[*].name}{"\n"}{end}`)
	if err != nil {
		return nil
	}
	var uids []string
	for _, line := range strings.Split(out, "\n") {
		parts := strings.SplitN(strings.TrimSpace(line), "|", 3)
		if len(parts) != 3 || parts[1] != "" {
			continue
		}
		for _, c := range strings.Fields(parts[2]) {
			if c == agentContainer {
				uids = append(uids, parts[0])
				break
			}
		}
	}
	return uids
}

// agentRemoved reports whether, in one successful listing of the pods of the
// workload name in ns, there is a non-terminating Ready pod without a
// traffic-agent container and no non-terminating pod with one.
func agentRemoved(ctx context.Context, r *rt.Runtime, ns, name string) bool {
	out, err := r.Kubectl(ctx, ns, "get", "pod", "-l", "app="+name, "-o",
		`jsonpath={range .items[*]}{.metadata.deletionTimestamp}|{.status.conditions[?(@.type=="Ready")].status}|{.spec.containers[*].name}{"\n"}{end}`)
	if err != nil {
		return false
	}
	readyWithoutAgent := false
	for _, line := range strings.Split(out, "\n") {
		parts := strings.SplitN(strings.TrimSpace(line), "|", 3)
		if len(parts) != 3 || parts[0] != "" {
			continue
		}
		if slices.Contains(strings.Fields(parts[2]), agentContainer) {
			return false
		}
		readyWithoutAgent = readyWithoutAgent || parts[1] == "True"
	}
	return readyWithoutAgent
}

// installAgents lets the default identity ingest each workload, which
// installs a traffic-agent in it, and then frees the default connection.
// The agents stay installed.
func installAgents(s *AuthEnforcing, ns string, wls ...*rt.Workload) {
	t := s.T()
	conn := s.Connect()
	for _, wl := range wls {
		a := conn.Ingest(t, wl, cli.MountFalse())
		a.Detach(t)
	}
	freeDefaultConnection(t, ns)
	for _, wl := range wls {
		s.Require().NotEmpty(agentPodUIDs(s.Ctx(), s.R(), ns, wl.Name), "%s should have a traffic-agent", wl.Name)
	}
}

// Test_UninstallWithoutGrantRefused connects as an identity that holds only
// the connect grant to a namespace where another identity installed an
// agent: uninstalling all agents or the workload by name is refused and the
// agent pod stays as it was.
func (s *AuthEnforcing) Test_UninstallWithoutGrantRefused() {
	t := s.T()
	ctx := s.Ctx()
	r := s.R()
	ns := s.AppNamespace()
	s.Manager()
	wl := s.Workload(workloads.Echo("auth-uninstall-nogrant"))
	installAgents(s, ns, wl)
	before := agentPodUIDs(ctx, r, ns, wl.Name)
	s.Require().NotEmpty(before, "%s should have a traffic-agent", wl.Name)

	tp := connectOnlyIdentity(t, ctx, r, "rtest-auth-uninstall-nogrant")
	quitDefensively(t, r, ctx, "AuthEnforcing:UninstallWithoutGrantRefused")

	_, stderr, err := tp.Run(ctx, connectArgs(ns)...)
	s.Require().NoError(err, "connect: %s", stderr)

	_, stderr, err = tp.Run(ctx, "uninstall", "--all-agents")
	s.Require().Error(err, "uninstalling all agents should be refused")
	s.Contains(stderr, "access to the whole namespace")

	_, stderr, err = tp.Run(ctx, "uninstall", wl.Name)
	s.Require().Error(err, "uninstalling %s should be refused", wl.Name)
	s.Contains(stderr, "is required")

	s.Never(func() bool {
		return !sameStrings(agentPodUIDs(ctx, r, ns, wl.Name), before)
	}, namespaceReadQuiet, namespaceReadPoll, "the agent pod of %s should stay", wl.Name)
}

// Test_UninstallWorkloadScopedGrant gives an identity create and get on the
// attachment of one workload (resourceNames) in a namespace where two
// workloads have agents: it can uninstall that workload only, and "all
// agents" is refused.
func (s *AuthEnforcing) Test_UninstallWorkloadScopedGrant() {
	t := s.T()
	ctx := s.Ctx()
	r := s.R()
	ns := s.AppNamespace()
	s.Manager()
	granted := s.Workload(workloads.Echo("auth-uninstall-granted"))
	other := s.Workload(workloads.Echo("auth-uninstall-other"))
	installAgents(s, ns, granted, other)
	otherBefore := agentPodUIDs(ctx, r, ns, other.Name)
	s.Require().NotEmpty(otherBefore, "%s should have a traffic-agent", other.Name)

	const name = "rtest-auth-uninstall-scoped"
	tp := connectOnlyIdentity(t, ctx, r, name)
	grantInNamespace(t, ctx, r, name, ns, workloadAttachmentRules(granted.Name))
	quitDefensively(t, r, ctx, "AuthEnforcing:UninstallWorkloadScopedGrant")

	_, stderr, err := tp.Run(ctx, connectArgs(ns)...)
	s.Require().NoError(err, "connect: %s", stderr)

	_, stderr, err = tp.Run(ctx, "uninstall", "--all-agents")
	s.Require().Error(err, "uninstalling all agents should be refused")
	s.Contains(stderr, "name the workloads")

	_, stderr, err = tp.Run(ctx, "uninstall", other.Name)
	s.Require().Error(err, "uninstalling %s should be refused", other.Name)
	s.Contains(stderr, "is required")
	s.Never(func() bool {
		return !sameStrings(agentPodUIDs(ctx, r, ns, other.Name), otherBefore)
	}, namespaceReadQuiet, namespaceReadPoll, "the agent pod of %s should stay", other.Name)
	s.NotEmpty(agentPodUIDs(ctx, r, ns, granted.Name), "refusals must not remove the agent of %s", granted.Name)

	_, stderr, err = tp.Run(ctx, "uninstall", granted.Name)
	s.Require().NoError(err, "uninstalling %s should succeed: %s", granted.Name, stderr)
	s.Require().Eventually(func() bool {
		return agentRemoved(ctx, r, ns, granted.Name)
	}, namespaceReadTimeout, namespaceReadPoll, "the agent of %s should be removed", granted.Name)
	s.NotEmpty(agentPodUIDs(ctx, r, ns, other.Name), "the agent of %s should stay", other.Name)
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	seen := make(map[string]int, len(a))
	for _, s := range a {
		seen[s]++
	}
	for _, s := range b {
		seen[s]--
	}
	for _, n := range seen {
		if n != 0 {
			return false
		}
	}
	return true
}
