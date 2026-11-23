package auth

import (
	"context"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/cli"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/managers"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/workloads"
)

// AgentidAgentIdentity drives the traffic-manager's ArriveAsAgent RPC
// directly, under enforcing mode, with the bound ServiceAccount token of an
// ordinary pod that runs no traffic-agent at all.
type AgentidAgentIdentity struct {
	rt.Suite
}

func init() {
	rt.Register(&AgentidAgentIdentity{}, rt.InArea("auth"), rt.NeedsManager(managers.AuthEnforcing()))
}

// agentidPodNameAndUID returns the name and UID of the one pod labeled
// app=appLabel in ns. The workload fixture has already waited for its
// rollout, so exactly one matching pod is expected.
func agentidPodNameAndUID(t *testing.T, ctx context.Context, r *rt.Runtime, ns, appLabel string) (name, uid string) {
	t.Helper()
	out, err := r.Kubectl(ctx, ns, "get", "pod", "-l", "app="+appLabel, "-o",
		`jsonpath={.items[0].metadata.name}|{.items[0].metadata.uid}`)
	if err != nil {
		t.Fatalf("getting pod for app=%s: %v", appLabel, err)
	}
	parts := strings.SplitN(strings.TrimSpace(out), "|", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		t.Fatalf("unexpected pod lookup output for app=%s: %q", appLabel, out)
	}
	return parts[0], parts[1]
}

// agentidBoundPodToken returns a token for sa, bound to the pod named
// podName with the given UID in ns, the default API-server audience and
// shape `kubectl create token` gives a Pod's mounted
// serviceaccount/token projection.
func agentidBoundPodToken(t *testing.T, ctx context.Context, r *rt.Runtime, ns, sa, podName, podUID string) string {
	t.Helper()
	out, err := r.Kubectl(ctx, ns, "create", "token", sa,
		"--bound-object-kind=Pod", "--bound-object-name="+podName, "--bound-object-uid="+podUID)
	if err != nil {
		t.Fatalf("creating bound token for pod %s: %v", podName, err)
	}
	return strings.TrimSpace(out)
}

// agentidInstallAgent connects as the suite's granted identity, installs a
// traffic-agent on wl via ingest, detaches, and frees the connection,
// leaving the agent running.
func agentidInstallAgent(s *AgentidAgentIdentity, wl *rt.Workload) {
	t := s.T()
	conn := s.Connect()
	a := conn.Ingest(t, wl, cli.MountFalse())
	a.Detach(t)
	freeDefaultConnection(t, s.AppNamespace())
}

// agentidFakeAgentInfo builds an otherwise-valid AgentInfo claiming to be
// the traffic-agent of the workload named claimedName/claimedKind in ns,
// running as the pod podName/podUID, with attacker-chosen pod IP and
// container environment.
func agentidFakeAgentInfo(ns, claimedName, claimedKind, podName, podUID string) *manager.AgentInfo {
	return &manager.AgentInfo{
		Name:      claimedName,
		Kind:      claimedKind,
		Namespace: ns,
		PodName:   podName,
		PodUid:    podUID,
		PodIp:     "6.6.6.6",
		ApiPort:   1,
		Product:   "telepresence",
		Version:   "v0.0.0",
		Mechanisms: []*manager.AgentInfo_Mechanism{
			{Name: "tcp", Product: "telepresence", Version: "v0.0.0"},
		},
		Containers: map[string]*manager.AgentInfo_ContainerInfo{
			"echo-server": {
				Environment: map[string]string{"AGENTID_FAKE_MARKER": "evil"},
			},
		},
	}
}

// Test_FakeAgentRegistrationRefused has an ordinary pod with no
// traffic-agent container -- holding only its own, default-audience bound
// ServiceAccount token -- call ArriveAsAgent claiming to be the agent of a
// different workload that does run one. The call must be refused with
// PermissionDenied.
func (s *AgentidAgentIdentity) Test_FakeAgentRegistrationRefused() {
	t := s.T()
	ctx := s.Ctx()
	r := s.R()
	ns := s.AppNamespace()
	s.Manager()

	victim := s.Workload(workloads.Echo("agentid-m1-victim"))
	agentidInstallAgent(s, victim)

	attacker := s.Workload(workloads.Echo("agentid-m1-attacker"))
	podName, podUID := agentidPodNameAndUID(t, ctx, r, ns, attacker.Name)
	tok := agentidBoundPodToken(t, ctx, r, ns, "default", podName, podUID)

	mc, closeFn, err := rt.ManagerClient(rt.Env{Ctx: ctx, T: t, R: r}, managers.ManagerNamespace)
	s.Require().NoError(err)
	defer closeFn()

	tokCtx := metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+tok)
	fake := agentidFakeAgentInfo(ns, victim.Name, "Deployment", podName, podUID)
	si, err := mc.ArriveAsAgent(tokCtx, fake)
	if err == nil {
		t.Cleanup(func() { _, _ = mc.Depart(tokCtx, si) })
	}
	s.Require().Error(err, "a pod registering as the agent of a workload it does not run should be refused")
	st, ok := status.FromError(err)
	s.Require().True(ok)
	s.Equal(codes.PermissionDenied, st.Code())
}

// Test_RealInterceptUnaffectedByFakeRegistration attempts the same fake
// registration as Test_FakeAgentRegistrationRefused against a second
// victim, then has the suite's granted identity intercept that victim
// through the CLI: the intercept's reported pod IP and environment must
// come from the real agent, never from the fake registration's values.
func (s *AgentidAgentIdentity) Test_RealInterceptUnaffectedByFakeRegistration() {
	t := s.T()
	ctx := s.Ctx()
	r := s.R()
	ns := s.AppNamespace()
	s.Manager()

	victim := s.Workload(workloads.Echo("agentid-m1-victim2"))
	agentidInstallAgent(s, victim)

	attacker := s.Workload(workloads.Echo("agentid-m1-attacker2"))
	podName, podUID := agentidPodNameAndUID(t, ctx, r, ns, attacker.Name)
	tok := agentidBoundPodToken(t, ctx, r, ns, "default", podName, podUID)

	mc, closeFn, err := rt.ManagerClient(rt.Env{Ctx: ctx, T: t, R: r}, managers.ManagerNamespace)
	s.Require().NoError(err)
	defer closeFn()

	tokCtx := metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+tok)
	fake := agentidFakeAgentInfo(ns, victim.Name, "Deployment", podName, podUID)
	si, err := mc.ArriveAsAgent(tokCtx, fake)
	if err == nil {
		t.Cleanup(func() { _, _ = mc.Depart(tokCtx, si) })
	}

	conn := s.Connect()
	ls := s.LocalEcho()
	a := conn.Intercept(t, victim, rt.ToLocal(ls, "http"), cli.MountFalse())
	defer a.Detach(t)

	s.Require().NotNil(a.Intercept)
	s.NotEqual("6.6.6.6", a.Intercept.PodIP, "the intercept's pod IP must come from the real agent")
	s.NotContains(a.Intercept.Environment, "AGENTID_FAKE_MARKER", "the fake registration's environment must not reach the intercept")
}

// Test_OwnWorkloadTokenWithoutManagerAudienceRefused has a plain pod --
// itself the only replica of its own workload, with no traffic-agent
// container -- call ArriveAsAgent honestly claiming to be its own
// workload's agent, using its default-audience bound token (the one
// mounted in an ordinary pod, as opposed to the traffic-manager-audience
// token the injector mounts only into a traffic-agent container). The call
// must be refused.
func (s *AgentidAgentIdentity) Test_OwnWorkloadTokenWithoutManagerAudienceRefused() {
	t := s.T()
	ctx := s.Ctx()
	r := s.R()
	ns := s.AppNamespace()
	s.Manager()

	lackey := s.Workload(workloads.Echo("agentid-m1-lackey"))
	podName, podUID := agentidPodNameAndUID(t, ctx, r, ns, lackey.Name)
	tok := agentidBoundPodToken(t, ctx, r, ns, "default", podName, podUID)

	mc, closeFn, err := rt.ManagerClient(rt.Env{Ctx: ctx, T: t, R: r}, managers.ManagerNamespace)
	s.Require().NoError(err)
	defer closeFn()

	tokCtx := metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+tok)
	honest := agentidFakeAgentInfo(ns, lackey.Name, "Deployment", podName, podUID)
	si, err := mc.ArriveAsAgent(tokCtx, honest)
	if err == nil {
		t.Cleanup(func() { _, _ = mc.Depart(tokCtx, si) })
	}
	s.Require().Error(err, "an agent registration without the traffic-manager audience should be refused in enforcing mode")
	st, ok := status.FromError(err)
	s.Require().True(ok)
	s.Equal(codes.PermissionDenied, st.Code())
}
