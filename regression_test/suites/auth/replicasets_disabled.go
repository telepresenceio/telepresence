package auth

import (
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
