package auth

import (
	"slices"
	"strings"
	"time"

	"github.com/telepresenceio/telepresence/v2/pkg/annotation"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/cli"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/managers"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/workloads"
)

// AgentidReplicaSetsDisabled runs enforcing authentication with
// workloads.replicaSets.enabled=false. The traffic-manager must still be
// able to read ReplicaSets to map an agent's pod to its Deployment.
type AgentidReplicaSetsDisabled struct {
	rt.Suite
}

// replicaSetsDisabledSpec returns managers.AuthEnforcing() with
// workloads.replicaSets.enabled=false.
func replicaSetsDisabledSpec() managers.Spec {
	off := false
	v := managers.AuthEnforcing().Values
	v.Workloads.ReplicaSets.Enabled = &off
	return managers.Spec{Key: "auth-enforcing-no-replicasets", Values: v}
}

func init() {
	rt.Register(&AgentidReplicaSetsDisabled{}, rt.InArea("auth"), rt.NeedsManager(replicaSetsDisabledSpec()))
}

// Test_InterceptDeployment intercepts a Deployment. Its traffic-agent is
// accepted only if the manager can get the pod's ReplicaSet, so the
// intercept becomes active and traffic reaches the local handler only then.
func (s *AgentidReplicaSetsDisabled) Test_InterceptDeployment() {
	t := s.T()
	wl := s.Workload(workloads.Echo("agentid-no-replicasets"))
	ls := s.LocalEcho()
	conn := s.Connect()
	a := conn.Intercept(t, wl, rt.ToLocal(ls, "http"), cli.MountFalse())
	t.Cleanup(func() { a.Detach(t) })
	rt.RoutedToLocal(t, wl.ServiceURL(), ls)
}

// AgentidReplicaSetsDisabledUpgrade switches a running traffic-manager to
// workloads.replicaSets.enabled=false and cluster-wide watching while a
// Deployment's pods already carry a traffic-agent.
type AgentidReplicaSetsDisabledUpgrade struct {
	rt.Suite
}

// replicaSetsDisabledClusterWideSpec returns replicaSetsDisabledSpec() with
// maxNamespaceSpecificWatchers=0, so the traffic-manager watches the whole
// cluster, as an installation without a namespace restriction does.
func replicaSetsDisabledClusterWideSpec() managers.Spec {
	zero := 0
	s := replicaSetsDisabledSpec()
	s.Key = "auth-enforcing-no-replicasets-cluster-wide"
	s.Values.MaxNamespaceSpecificWatchers = &zero
	return s
}

func init() {
	rt.Register(&AgentidReplicaSetsDisabledUpgrade{}, rt.InArea("auth"), rt.NeedsManager(managers.AuthEnforcing()))
}

const (
	upgradeAgentTimeout = 90 * time.Second
	upgradeReadyTimeout = 2 * time.Minute
	upgradePollInterval = 2 * time.Second
)

// Test_ManagerStartsWithAgentPods injects a traffic-agent into a Deployment's
// pods while ReplicaSets are enabled, then switches the traffic-manager to
// cluster-wide watching with ReplicaSets disabled. The traffic-manager must
// become ready without restarting, and must still accept the agent of the
// existing pod.
func (s *AgentidReplicaSetsDisabledUpgrade) Test_ManagerStartsWithAgentPods() {
	t := s.T()
	ctx := s.Ctx()
	r := s.R()
	ns := s.AppNamespace()

	tpl := workloads.Echo("agentid-rs-disabled-upgrade")
	tpl.Annotations = map[string]string{annotation.InjectTrafficAgent: "enabled"}
	wl := s.Workload(tpl)
	s.Require().Eventually(func() bool {
		out, err := r.Kubectl(ctx, ns, "get", "pod", "-l", "app="+tpl.Name, "-o", "jsonpath={.items[*].spec.containers[*].name}")
		return err == nil && slices.Contains(strings.Fields(out), "traffic-agent")
	}, upgradeAgentTimeout, upgradePollInterval, "the pods of %s should carry a traffic-agent", tpl.Name)

	conn := switchManagerSpec(t, ctx, replicaSetsDisabledClusterWideSpec(), ns)

	s.Require().Eventually(func() bool {
		out, err := r.Kubectl(ctx, managers.ManagerNamespace, "get", "pod", "traffic-manager-0", "-o",
			"jsonpath={.status.containerStatuses[0].ready} {.status.containerStatuses[0].restartCount}")
		return err == nil && strings.TrimSpace(out) == "true 0"
	}, upgradeReadyTimeout, upgradePollInterval, "traffic-manager-0 should be ready with restart count 0")

	ls := s.LocalEcho()
	a := conn.Intercept(t, wl, rt.ToLocal(ls, "http"), cli.MountFalse())
	t.Cleanup(func() { a.Detach(t) })
	rt.RoutedToLocal(t, wl.ServiceURL(), ls)
}
