package auth

import (
	"strings"

	"github.com/telepresenceio/telepresence/v2/regression_test/framework/managers"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/workloads"
)

// AgentidNodeAgentEnforcing runs a node-agent under enforcing authentication:
// the traffic-manager must accept the node-agent's registration (audience,
// pod and Job checks), and the node-agent must serve a client that holds the
// attachment grant over its tunnel and its file servers.
type AgentidNodeAgentEnforcing struct {
	rt.Suite
}

func init() {
	rt.Register(&AgentidNodeAgentEnforcing{}, rt.InArea("auth"), rt.NeedsManager(managers.NodeAgentAuthEnforcing()),
		rt.Requires(rt.FUSE), rt.NotOn("windows"))
}

// Test_MountedInterceptOverNodeAgent intercepts EchoWithConfigVolume in
// node-agent mode as the granted default identity, once per mount transport
// (FTP, then SFTP). Each time the intercept must become active, traffic must
// reach the local listener and the mounted ConfigMap file must be readable.
// It also asserts that a node-agent Job backs the workload and that no
// sidecar was injected, so the node-agent registration is what is tested.
func (s *AgentidNodeAgentEnforcing) Test_MountedInterceptOverNodeAgent() {
	t := s.T()
	ctx := s.Ctx()
	r := s.R()
	ns := s.AppNamespace()
	s.Manager()

	wl := s.Workload(workloads.EchoWithConfigVolume("agentid-nodeagent-mounts"))
	ls := s.LocalEcho()

	for _, useFtp := range []bool{true, false} {
		agentidMountedIntercept(t, ns, wl, ls, useFtp, agentidNodeAgentFlag())
		if t.Failed() {
			return
		}
	}

	selector := "app=traffic-node-agent,telepresence.io/agentName=" + wl.Name + ",telepresence.io/workloadNamespace=" + wl.Namespace
	out, err := r.Kubectl(ctx, managers.ManagerNamespace, "get", "jobs", "-l", selector, "-o", "name")
	s.Require().NoError(err)
	s.NotEmpty(strings.TrimSpace(out), "a node-agent Job should have backed the intercept of %s", wl.Name)
	pods, err := r.Kubectl(ctx, ns, "get", "pods", "-l", "app="+wl.Name, "-o", "jsonpath={.items[*].spec.containers[*].name}")
	s.Require().NoError(err)
	s.NotContains(strings.Fields(pods), "traffic-agent", "a node-agent intercept must not inject a sidecar")
}
