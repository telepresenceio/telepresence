package auth

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/cli"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/managers"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/workloads"
)

// mgrapiConnectOnlyRules grants create on connections.telepresence.io and
// nothing else.
const mgrapiConnectOnlyRules = `  - apiGroups: ["telepresence.io"]
    resources: ["connections"]
    verbs: ["create"]`

// MgrapiEnforcing proves that the traffic-manager in enforcing mode limits
// token reviews before authentication, refuses GetIntercept for an
// intercept the caller does not own, and sends no traffic-agent pod names to
// a caller that has no logs grant.
type MgrapiEnforcing struct {
	rt.Suite
}

func init() {
	rt.Register(&MgrapiEnforcing{}, rt.InArea("auth"), rt.NeedsManager(managers.AuthEnforcing()))
}

func (s *MgrapiEnforcing) arrive(ctx context.Context, mc manager.ManagerClient, ns, name, tok string) (*manager.SessionInfo, error) {
	tokCtx := metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+tok)
	return mc.ArriveAsClient(tokCtx, &manager.ClientInfo{
		Name:      name,
		Namespace: ns,
		InstallId: name,
		Product:   "telepresence",
		Version:   "v" + s.R().Version().String(),
	})
}

// Test_UnknownTokenFloodIsLimited sends distinct unknown bearer tokens to
// the traffic-manager until a call is rejected with ResourceExhausted, and
// asserts that no call is admitted and that a caller with a valid token is
// admitted once the burst is over. The limit is only reachable when the
// calls arrive faster than its budget refills, so a cluster too slow to do
// that skips the test instead of failing it.
func (s *MgrapiEnforcing) Test_UnknownTokenFloodIsLimited() {
	const (
		workers  = 32
		maxWait  = 90 * time.Second
		budgetQS = 50  // sustained reviews per second the manager admits
		burst    = 100 // reviews admitted before the sustained rate applies
	)
	t := s.T()
	ctx := s.Ctx()
	r := s.R()
	ns := s.AppNamespace()
	s.Manager()
	mc, closeFn, err := rt.ManagerClient(rt.Env{Ctx: ctx, T: t, R: r}, managers.ManagerNamespace)
	s.Require().NoError(err)
	defer closeFn()

	var (
		mu     sync.Mutex
		counts = map[codes.Code]int{}
		wg     sync.WaitGroup
	)
	fctx, stop := context.WithTimeout(ctx, maxWait)
	defer stop()
	start := time.Now()
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for fctx.Err() == nil {
				cctx, cancel := context.WithTimeout(fctx, 30*time.Second)
				_, err := s.arrive(cctx, mc, ns, "mgrapi-flood", uuid.NewString())
				cancel()
				if fctx.Err() != nil {
					return
				}
				code := status.Code(err)
				mu.Lock()
				counts[code]++
				limited := code == codes.ResourceExhausted
				mu.Unlock()
				if limited {
					stop()
				}
			}
		}()
	}
	wg.Wait()
	elapsed := time.Since(start)

	total := 0
	for _, n := range counts {
		total += n
	}
	s.Equal(0, counts[codes.OK], "no unknown token may be admitted: %v", counts)
	if counts[codes.ResourceExhausted] == 0 {
		if float64(total) <= burst+budgetQS*elapsed.Seconds() {
			s.T().Skipf("only %d calls in %s: too slow to exceed the review budget: %v", total, elapsed, counts)
		}
		s.Failf("no call was limited", "%d calls in %s, budget %d + %d/s: %v", total, elapsed, burst, budgetQS, counts)
	}

	tok := kubectlCreateToken(t, ctx, r, managers.TestServiceAccount)
	s.Require().Eventually(func() bool {
		cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		si, err := s.arrive(cctx, mc, ns, "mgrapi-after-flood", tok)
		if err != nil {
			return false
		}
		_, _ = mc.Depart(metadata.AppendToOutgoingContext(cctx, "authorization", "Bearer "+tok), si)
		return true
	}, 45*time.Second, 2*time.Second, "a valid caller should be admitted after the burst")
}

// Test_GetInterceptRefusedForForeignIntercept creates an intercept as the
// default identity and asks for it by its full ID as another authenticated
// identity, without a session and with its own session. Both calls must be
// refused with NotFound.
func (s *MgrapiEnforcing) Test_GetInterceptRefusedForForeignIntercept() {
	t := s.T()
	ctx := s.Ctx()
	r := s.R()
	ns := s.AppNamespace()
	s.Manager()
	wl := s.Workload(workloads.Echo("mgrapi-get-intercept"))
	ls := s.LocalEcho()
	conn := s.Connect()

	a := conn.Intercept(t, wl, rt.ToLocal(ls, "http"), cli.MountFalse())
	defer a.Detach(t)
	s.Require().NotNil(a.Intercept)
	s.Require().NotEmpty(a.Intercept.ID)

	name := createGrantIdentity(t, ctx, r, "rtest-mgrapi-get-intercept", telepresenceGrantRules)
	tok := kubectlCreateToken(t, ctx, r, name)
	mc, tokCtx, si := dialAndArrive(t, ctx, r, ns, name, tok)

	ii, err := mc.GetIntercept(tokCtx, &manager.GetInterceptRequest{Name: a.Intercept.ID})
	s.Require().Error(err, "GetIntercept without a session: %v", ii)
	s.Equal(codes.NotFound, status.Code(err))

	ii, err = mc.GetIntercept(tokCtx, &manager.GetInterceptRequest{Session: si, Name: a.Intercept.ID})
	s.Require().Error(err, "GetIntercept with the caller's own session: %v", ii)
	s.Equal(codes.NotFound, status.Code(err))
}

// Test_StreamLogsNamesNoPodsWithoutLogsGrant installs a traffic-agent and
// streams agent logs as an identity that holds only the connect grant. The
// stream must end cleanly without a frame that names any pod.
func (s *MgrapiEnforcing) Test_StreamLogsNamesNoPodsWithoutLogsGrant() {
	t := s.T()
	ctx := s.Ctx()
	r := s.R()
	ns := s.AppNamespace()
	s.Manager()
	wl := s.Workload(workloads.Echo("mgrapi-logs"))

	conn := s.Connect()
	a := conn.Ingest(t, wl, cli.MountFalse())
	a.Detach(t)
	freeDefaultConnection(t, ns)
	s.Require().Eventually(func() bool {
		return len(agentPodUIDs(ctx, r, ns, wl.Name)) > 0
	}, 2*time.Minute, 2*time.Second, "%s should have a traffic-agent", wl.Name)
	out, err := r.Kubectl(ctx, ns, "get", "pod", "-l", "app="+wl.Name, "-o", `jsonpath={.items[*].metadata.name}`)
	s.Require().NoError(err)
	podNames := strings.Fields(out)
	s.Require().NotEmpty(podNames)

	name := createGrantIdentity(t, ctx, r, "rtest-mgrapi-logs", mgrapiConnectOnlyRules)
	tok := kubectlCreateToken(t, ctx, r, name)
	mc, tokCtx, si := dialAndArrive(t, ctx, r, ns, name, tok)

	sctx, cancel := context.WithTimeout(tokCtx, time.Minute)
	defer cancel()
	stream, err := mc.StreamLogs(sctx, &manager.StreamLogsRequest{Session: si, Agents: "all"})
	s.Require().NoError(err)
	for {
		chunk, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		s.Require().NoError(err)
		s.Empty(chunk.GetPodName(), "no frame may name a pod: %v", chunk)
		s.Empty(chunk.GetPodNamespace(), "no frame may name a pod namespace: %v", chunk)
		for _, pod := range podNames {
			s.NotContains(chunk.GetError(), pod)
		}
	}
}
