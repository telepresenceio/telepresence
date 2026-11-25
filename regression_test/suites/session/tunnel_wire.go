package session

import (
	"context"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/telepresenceio/telepresence/rpc/v2/agent"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/cli"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/managers"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/workloads"
)

const (
	tunnelwireCallTimeout  = 30 * time.Second
	tunnelwireSettle       = 5 * time.Second
	tunnelwireReadyTimeout = 120 * time.Second
	tunnelwireAgentWait    = 60 * time.Second
	tunnelwireAgentPoll    = 2 * time.Second
	tunnelwireManagerSel   = "app=traffic-manager"
	tunnelwireManagerCtr   = "traffic-manager"
	tunnelwireAgentCtr     = "traffic-agent"
)

// TunnelwireTunnel sends malformed first tunnel messages to the Tunnel call of the
// traffic-manager and of a traffic-agent, without credentials, and requires the call to
// be refused while the container keeps running.
type TunnelwireTunnel struct {
	rt.Suite
}

func init() {
	rt.Register(&TunnelwireTunnel{}, rt.InArea("session"), rt.NeedsManager(managers.AuthPermissive()))
}

// tunnelwireTunneler is the client side of either Tunnel call.
type tunnelwireTunneler func(ctx context.Context) (grpc.BidiStreamingClient[manager.TunnelMessage, manager.TunnelMessage], error)

// tunnelwireTarget is a container that must survive the attack.
type tunnelwireTarget struct {
	ns, selector, container string
}

func (s *TunnelwireTunnel) states(tg tunnelwireTarget) map[string]rt.TunnelwireContainerState {
	st, err := rt.TunnelwireContainerStates(s.Ctx(), s.R(), tg.ns, tg.selector, tg.container)
	s.Require().NoError(err)
	s.Require().NotEmpty(st, "no pod matches %s in %s", tg.selector, tg.ns)
	return st
}

// attack sends each message on a stream of its own and requires FailedPrecondition from
// the first Recv, then that the container's pods have the same restart count and are
// Ready.
func (s *TunnelwireTunnel) attack(tg tunnelwireTarget, open tunnelwireTunneler, msgs []*manager.TunnelMessage) {
	before := s.states(tg)
	s.T().Cleanup(func() { s.awaitReady(tg) })
	for i, m := range msgs {
		ctx, cancel := context.WithTimeout(s.Ctx(), tunnelwireCallTimeout)
		stream, err := open(ctx)
		s.Require().NoError(err)
		s.Require().NoError(stream.Send(m))
		_, err = stream.Recv()
		cancel()
		s.Require().Error(err, "message %d was accepted", i)
		s.Require().Equal(codes.FailedPrecondition, status.Code(err), "message %d: %v", i, err)
	}
	time.Sleep(tunnelwireSettle)
	s.Require().Equal(before, s.states(tg), "the container restarted or stopped being Ready")
}

func (s *TunnelwireTunnel) awaitReady(tg tunnelwireTarget) {
	s.Eventually(func() bool {
		st, err := rt.TunnelwireContainerStates(s.Ctx(), s.R(), tg.ns, tg.selector, tg.container)
		if err != nil || len(st) == 0 {
			return false
		}
		for _, p := range st {
			if p.Ready != "True" {
				return false
			}
		}
		return true
	}, tunnelwireReadyTimeout, tunnelwireAgentPoll, "pods matching %s never became Ready", tg.selector)
}

func (s *TunnelwireTunnel) managerSession() (manager.ManagerClient, *manager.SessionInfo) {
	t := s.T()
	env := rt.Env{Ctx: s.Ctx(), T: t, R: s.R()}
	mc, closeMC, err := rt.ManagerClient(env, managers.ManagerNamespace)
	s.Require().NoError(err)
	t.Cleanup(closeMC)
	ctx, cancel := context.WithCancel(s.Ctx())
	t.Cleanup(cancel)
	return mc, arriveAsClient(t, ctx, mc, "rtest-tunnelwire", s.AppNamespace())
}

func (s *TunnelwireTunnel) managerTarget() tunnelwireTarget {
	return tunnelwireTarget{ns: managers.ManagerNamespace, selector: tunnelwireManagerSel, container: tunnelwireManagerCtr}
}

// agentTunneler installs a traffic-agent in a fresh workload and returns a Tunnel
// opener connected to its pod, along with the pod's container target.
func (s *TunnelwireTunnel) agentTunneler(name string) (tunnelwireTunneler, tunnelwireTarget, *manager.SessionInfo) {
	t := s.T()
	ctx := s.Ctx()
	ns := s.AppNamespace()
	conn := s.Connect()
	wl := s.Workload(workloads.Echo(name))
	conn.Ingest(t, wl, cli.MountFalse()).Detach(t)

	mc, si := s.managerSession()
	watchCtx, cancel := context.WithCancel(ctx)
	t.Cleanup(cancel)
	stream, err := mc.WatchAgentPods(watchCtx, si)
	s.Require().NoError(err)
	found := make(chan *manager.AgentPodInfo, 1)
	go func() {
		for {
			snap, err := stream.Recv()
			if err != nil {
				return
			}
			for _, ai := range snap.GetAgents() {
				if ai.GetWorkloadName() == wl.Name && ai.GetNamespace() == ns {
					select {
					case found <- ai:
					default:
					}
					return
				}
			}
		}
	}()
	var ai *manager.AgentPodInfo
	select {
	case ai = <-found:
	case <-time.After(tunnelwireAgentWait):
		s.Require().FailNow("the agent of " + wl.Name + " never registered")
	}

	env := rt.Env{Ctx: ctx, T: t, R: s.R()}
	gc, err := rt.TunnelwireDialPod(env, ns, ai.GetPodName(), uint16(ai.GetApiPort()))
	s.Require().NoError(err)
	t.Cleanup(func() { _ = gc.Close() })
	ac := agent.NewAgentClient(gc)
	open := func(ctx context.Context) (grpc.BidiStreamingClient[manager.TunnelMessage, manager.TunnelMessage], error) {
		return ac.Tunnel(ctx)
	}
	tg := tunnelwireTarget{ns: ns, selector: "app=" + wl.Name, container: tunnelwireAgentCtr}
	s.Eventually(func() bool {
		st, err := rt.TunnelwireContainerStates(ctx, s.R(), tg.ns, tg.selector, tg.container)
		return err == nil && len(st) == 1
	}, tunnelwireReadyTimeout, tunnelwireAgentPoll, "the pods of %s never settled on one", wl.Name)
	s.awaitReady(tg)
	return open, tg, si
}

// Test_ManagerRefusesTruncatedFirstMessage requires the manager's Tunnel to answer
// FailedPrecondition to first messages whose declared ConnID length overruns the message,
// and the traffic-manager container to stay up.
func (s *TunnelwireTunnel) Test_ManagerRefusesTruncatedFirstMessage() {
	s.Manager()
	mc, _ := s.managerSession()
	open := func(ctx context.Context) (grpc.BidiStreamingClient[manager.TunnelMessage, manager.TunnelMessage], error) {
		return mc.Tunnel(ctx)
	}
	s.attack(s.managerTarget(), open, rt.TunnelwireTruncatedIDMessages())
}

// Test_ManagerRefusesBadConnIDLength requires the manager's Tunnel to answer
// FailedPrecondition to first messages from a valid session carrying a ConnID of an
// invalid length, and the traffic-manager container to stay up.
func (s *TunnelwireTunnel) Test_ManagerRefusesBadConnIDLength() {
	s.Manager()
	mc, si := s.managerSession()
	open := func(ctx context.Context) (grpc.BidiStreamingClient[manager.TunnelMessage, manager.TunnelMessage], error) {
		return mc.Tunnel(ctx)
	}
	s.attack(s.managerTarget(), open, rt.TunnelwireBadConnIDMessages(si.GetSessionId()))
}

// Test_AgentRefusesTruncatedFirstMessage requires a traffic-agent's Tunnel to answer
// FailedPrecondition to first messages whose declared ConnID length overruns the
// message, and the traffic-agent container to stay up.
func (s *TunnelwireTunnel) Test_AgentRefusesTruncatedFirstMessage() {
	s.Manager()
	open, tg, _ := s.agentTunneler("tunnelwire-agent-trunc")
	s.attack(tg, open, rt.TunnelwireTruncatedIDMessages())
}

// Test_AgentRefusesBadConnIDLength requires a traffic-agent's Tunnel to answer
// FailedPrecondition to first messages carrying a ConnID of an invalid length, and the
// traffic-agent container to stay up.
func (s *TunnelwireTunnel) Test_AgentRefusesBadConnIDLength() {
	s.Manager()
	open, tg, si := s.agentTunneler("tunnelwire-agent-connid")
	s.attack(tg, open, rt.TunnelwireBadConnIDMessages(si.GetSessionId()))
}
