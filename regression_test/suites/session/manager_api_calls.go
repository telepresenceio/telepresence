package session

import (
	"context"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/miekg/dns"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/cli"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/managers"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/workloads"
)

const (
	mgrapiExcludedHost     = "TP_TEST_DB_HOST"
	mgrapiExcludedPassword = "TP_TEST_DB_PASSWORD"
	mgrapiKeep             = "TP_TEST_KEEP"
)

// mgrapiSpec excludes two environment variables from agent reports. It is
// the same spec as the intercept area's env-excluded suite, so the release
// is shared.
//
//nolint:gochecknoglobals // registered once at init time, like every other manager spec
var mgrapiSpec = managers.Spec{
	Key: "env-excluded",
	Values: managers.Values{
		Security: managers.Security{Authentication: managers.Authentication{Mode: "permissive"}},
		Intercept: managers.Intercept{
			Environment: managers.InterceptEnvironment{
				Excluded: []string{mgrapiExcludedHost, mgrapiExcludedPassword},
			},
		},
	},
}

// MgrapiManagerCalls proves that the traffic-manager's reconnect, DNS lookup
// and agent-environment handling hold up against callers that send
// malformed or hostile requests over raw gRPC.
type MgrapiManagerCalls struct {
	rt.Suite
}

func init() {
	rt.Register(&MgrapiManagerCalls{}, rt.InArea("session"), rt.NeedsManager(mgrapiSpec))
}

func (s *MgrapiManagerCalls) dial() (manager.ManagerClient, context.Context, context.CancelFunc) {
	t := s.T()
	mc, closeFn, err := rt.ManagerClient(rt.Env{Ctx: s.Ctx(), T: t, R: s.R()}, managers.ManagerNamespace)
	s.Require().NoError(err)
	t.Cleanup(closeFn)
	ctx, cancel := context.WithCancel(s.Ctx())
	t.Cleanup(cancel)
	return mc, ctx, cancel
}

func mgrapiDepart(mc manager.ManagerClient, id string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, _ = mc.Depart(ctx, &manager.SessionInfo{SessionId: id})
}

// Test_ReconnectCannotPickForeignSessionIDs asserts that a caller cannot
// register an agent under a live client session's ID or another agent's
// session ID (PermissionDenied), that a client reconnect cannot use an
// agent-form session ID (InvalidArgument), and that the client session
// stays usable.
func (s *MgrapiManagerCalls) Test_ReconnectCannotPickForeignSessionIDs() {
	t := s.T()
	ns := s.AppNamespace()
	s.Manager()
	mc, ctx, _ := s.dial()

	victim := arriveAsClient(t, ctx, mc, "mgrapi-victim", ns)
	suffix := uuid.NewString()[:8]
	agent := mgrapiAgentInfo(ns, "mgrapi-agent-"+suffix, "mgrapi-uid-"+suffix)
	otherAgentSession := "agent:mgrapi-other-" + suffix
	t.Cleanup(func() { mgrapiDepart(mc, otherAgentSession) })

	for _, id := range []string{victim.SessionId, otherAgentSession} {
		_, err := mc.ReconnectAgent(ctx, &manager.ReconnectAgentRequest{
			Session: &manager.SessionInfo{SessionId: id},
			Agent:   agent,
		})
		s.Require().Error(err, "ReconnectAgent with session %q", id)
		s.Equal(codes.PermissionDenied, status.Code(err), "ReconnectAgent with session %q: %v", id, err)
	}

	_, err := mc.Remain(ctx, &manager.RemainRequest{Session: victim})
	s.Require().NoError(err, "the client session must still be usable")

	agentForm := "agent:mgrapi-client-" + suffix
	t.Cleanup(func() { mgrapiDepart(mc, agentForm) })
	_, err = mc.ReconnectClient(ctx, &manager.ReconnectClientRequest{
		Session: &manager.SessionInfo{SessionId: agentForm},
		Client:  mgrapiClientInfo(s.R(), "mgrapi-agent-form", ns),
	})
	s.Require().Error(err, "ReconnectClient with an agent-form session ID")
	s.Equal(codes.InvalidArgument, status.Code(err))
}

// Test_ConcurrentReconnectClientSameSession asserts that many simultaneous
// ReconnectClient calls with one new session ID all succeed and leave the
// traffic-manager running without a restart.
func (s *MgrapiManagerCalls) Test_ConcurrentReconnectClientSameSession() {
	const (
		rounds  = 20
		callers = 64
	)
	t := s.T()
	r := s.R()
	ns := s.AppNamespace()
	s.Manager()
	mc, ctx, _ := s.dial()
	before := mgrapiWatchManager(t, ctx, r)

	info := mgrapiClientInfo(r, "mgrapi-racing", ns)
	for range rounds {
		id := uuid.NewString()
		start := make(chan struct{})
		errs := make([]error, callers)
		var wg sync.WaitGroup
		for i := range errs {
			wg.Add(1)
			go func() {
				defer wg.Done()
				cctx, cancel := context.WithTimeout(ctx, mgrapiCallTimeout)
				defer cancel()
				<-start
				_, errs[i] = mc.ReconnectClient(cctx, &manager.ReconnectClientRequest{
					Session: &manager.SessionInfo{SessionId: id},
					Client:  info,
				})
			}()
		}
		close(start)
		wg.Wait()
		for _, err := range errs {
			s.Require().NoError(err, "ReconnectClient with session %s", id)
		}
		mgrapiDepart(mc, id)
	}
	mgrapiRequireManagerIntact(t, ctx, r, before)
}

// Test_ReconnectClientWithoutSessionRefused asserts that a ReconnectClient
// that carries an intercept but no session ID is refused with
// InvalidArgument, and that another client's watch, remain and departure
// still work and the traffic-manager does not restart.
func (s *MgrapiManagerCalls) Test_ReconnectClientWithoutSessionRefused() {
	t := s.T()
	r := s.R()
	ns := s.AppNamespace()
	s.Manager()
	wl := s.Workload(workloads.Echo("mgrapi-reconnect-nosession"))
	mc, ctx, _ := s.dial()
	before := mgrapiWatchManager(t, ctx, r)

	victim := arriveAsClient(t, ctx, mc, "mgrapi-victim", ns)

	spec := &manager.InterceptSpec{
		Name:         wl.Name,
		Client:       "mgrapi-no-session",
		Agent:        wl.Name,
		Namespace:    ns,
		WorkloadKind: "Deployment",
		Mechanism:    "tcp",
		TargetHost:   "127.0.0.1",
		TargetPort:   8080,
	}
	for _, session := range []*manager.SessionInfo{nil, {}} {
		_, err := mc.ReconnectClient(ctx, &manager.ReconnectClientRequest{
			Session:    session,
			Client:     mgrapiClientInfo(r, "mgrapi-no-session", ns),
			Intercepts: []*manager.InterceptInfo{{Spec: spec}},
		})
		s.Require().Error(err)
		s.Equal(codes.InvalidArgument, status.Code(err))
	}

	wctx, wcancel := context.WithTimeout(ctx, mgrapiCallTimeout)
	defer wcancel()
	stream, err := mc.WatchIntercepts(wctx, victim)
	s.Require().NoError(err)
	_, err = stream.Recv()
	s.Require().NoError(err, "the victim's WatchIntercepts must deliver its snapshot")
	wcancel()

	_, err = mc.Remain(ctx, &manager.RemainRequest{Session: victim})
	s.Require().NoError(err)
	_, err = mc.Depart(ctx, victim)
	s.Require().NoError(err)
	mgrapiRequireManagerIntact(t, ctx, r, before)
}

// Test_LookupDNSRejectsEmptyName asserts that LookupDNS with an empty or
// dot-only name is refused with InvalidArgument for each record type, and
// that the traffic-manager does not restart.
func (s *MgrapiManagerCalls) Test_LookupDNSRejectsEmptyName() {
	t := s.T()
	r := s.R()
	ns := s.AppNamespace()
	s.Manager()
	mc, ctx, _ := s.dial()
	before := mgrapiWatchManager(t, ctx, r)

	sess := arriveAsClient(t, ctx, mc, "mgrapi-dns", ns)
	for _, name := range []string{"", "."} {
		for _, qType := range []uint16{dns.TypeA, dns.TypeMX, dns.TypeNS, dns.TypeSRV} {
			cctx, cancel := context.WithTimeout(ctx, mgrapiCallTimeout)
			_, err := mc.LookupDNS(cctx, &manager.DNSRequest{Session: sess, Name: name, Type: uint32(qType)})
			cancel()
			s.Require().Error(err, "LookupDNS name %q type %d", name, qType)
			s.Equal(codes.InvalidArgument, status.Code(err), "LookupDNS name %q type %d: %v", name, qType, err)
		}
	}
	mgrapiRequireManagerIntact(t, ctx, r, before)
}

// mgrapiRequireExcludedAbsent asserts env carries the kept variable and
// neither excluded one.
func (s *MgrapiManagerCalls) mgrapiRequireExcludedAbsent(env map[string]string, when string) {
	s.T().Helper()
	s.Equal("DATA", env[mgrapiKeep], "%s: the kept variable must be reported", when)
	for _, name := range []string{mgrapiExcludedHost, mgrapiExcludedPassword} {
		s.NotContains(env, name, "%s: excluded variable %s must not be reported", when, name)
	}
}

// Test_ExcludedEnvSurvivesManagerRestart asserts that variables on the
// exclusion list are absent from the environment an ingest reports, both
// before and after the traffic-manager restarts and the workload's agent
// reconnects to it.
func (s *MgrapiManagerCalls) Test_ExcludedEnvSurvivesManagerRestart() {
	t := s.T()
	ctx := s.Ctx()
	r := s.R()
	ns := s.AppNamespace()

	tpl := workloads.Echo("mgrapi-env-restart")
	tpl.Env = map[string]string{
		mgrapiExcludedHost:     "db",
		mgrapiExcludedPassword: "hunter2",
		mgrapiKeep:             "DATA",
	}
	wl := s.Workload(tpl)
	conn := s.Connect()

	a := conn.Ingest(t, wl, cli.MountFalse())
	s.Require().NotNil(a.Ingest)
	s.mgrapiRequireExcludedAbsent(a.Ingest.Environment, "before the restart")
	a.Detach(t)

	freeDefaultConnection(t, ns)
	s.Require().NoError(rt.RestartManager(rt.Env{Ctx: ctx, T: t, R: r}))
	conn = s.Connect()

	s.Require().Eventually(func() bool {
		for _, e := range conn.ListNamespace(t, ns) {
			if e.Name == wl.Name {
				return e.AgentVersion != ""
			}
		}
		return false
	}, 2*time.Minute, 2*time.Second, "the agent of %s should reconnect to the restarted manager", wl.Name)

	a = conn.Ingest(t, wl, cli.MountFalse())
	s.Require().NotNil(a.Ingest)
	s.mgrapiRequireExcludedAbsent(a.Ingest.Environment, "after the restart")
	a.Detach(t)
}
