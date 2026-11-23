package auth

import (
	"strings"
	"time"

	"github.com/telepresenceio/telepresence/v2/regression_test/framework/managers"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/workloads"
)

// agentidUnimplementedLog is the line a traffic-agent logs when its
// traffic-manager answers AuthorizeSessionAccess with Unimplemented.
const agentidUnimplementedLog = "doesn't implement AuthorizeSessionAccess"

// AgentidPreAccessCallManager runs a current traffic-agent against an
// enforcing traffic-manager that answers Unimplemented for
// AuthorizeSessionAccess, as a manager older than 2.34 does.
type AgentidPreAccessCallManager struct {
	rt.Suite
}

func init() {
	rt.Register(&AgentidPreAccessCallManager{}, rt.InArea("auth"), rt.NeedsManager(managers.AgentidEnforcingPreAccessCall()),
		rt.Requires(rt.FUSE), rt.NotOn("windows"))
}

// Test_MountedInterceptFallsBackToSessionCredential intercepts
// EchoWithConfigVolume as the granted default identity, once per mount
// transport (FTP, then SFTP). Each time the intercept must become active,
// traffic must reach the local listener and the mounted ConfigMap file must
// be readable: the agent must treat "not implemented" as "the session
// credential decides", not as "no access". The agent's log must also show
// that it took the fallback, so the test cannot pass without the call
// having been refused as unimplemented.
func (s *AgentidPreAccessCallManager) Test_MountedInterceptFallsBackToSessionCredential() {
	t := s.T()
	ctx := s.Ctx()
	r := s.R()
	ns := s.AppNamespace()
	s.Manager()

	wl := s.Workload(workloads.EchoWithConfigVolume("agentid-compat-mounts"))
	ls := s.LocalEcho()

	for _, useFtp := range []bool{true, false} {
		agentidMountedIntercept(t, ns, wl, ls, useFtp)
		if t.Failed() {
			return
		}
	}

	s.Require().Eventually(func() bool {
		log, err := agentidAgentLogs(ctx, r, ns, wl.Name)
		return err == nil && strings.Contains(log, agentidUnimplementedLog)
	}, agentidMountTimeout, time.Second, "the traffic-agent of %s should log that the traffic-manager doesn't implement AuthorizeSessionAccess", wl.Name)
}
