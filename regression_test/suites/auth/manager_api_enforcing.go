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

func (s *MgrapiEnforcing) arrive(ctx context.Context, mc manager.ManagerClient, name, tok string) (*manager.SessionInfo, error) {
	tokCtx := metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+tok)
	return mc.ArriveAsClient(tokCtx, &manager.ClientInfo{
		Name:      name,
		Namespace: s.AppNamespace(),
		InstallId: name,
		Product:   "telepresence",
		Version:   "v" + s.R().Version().String(),
	})
}

// Test_UnknownTokenFloodIsLimited sends a burst of distinct unknown bearer
// tokens to the traffic-manager and asserts that every call is rejected,
// that some are rejected with ResourceExhausted, and that a caller with a
// valid token is admitted once the burst is over.
func (s *MgrapiEnforcing) Test_UnknownTokenFloodIsLimited() {
	const (
		attempts = 300
		workers  = 32
	)
	t := s.T()
	ctx := s.Ctx()
	r := s.R()
	s.Manager()
	mc, closeFn, err := rt.ManagerClient(rt.Env{Ctx: ctx, T: t, R: r}, managers.ManagerNamespace)
	s.Require().NoError(err)
	defer closeFn()

	var (
		mu     sync.Mutex
		counts = map[codes.Code]int{}
		wg     sync.WaitGroup
		jobs   = make(chan struct{})
	)
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range jobs {
				cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
				_, err := s.arrive(cctx, mc, "mgrapi-flood", uuid.NewString())
				cancel()
				mu.Lock()
				counts[status.Code(err)]++
				mu.Unlock()
			}
		}()
	}
	for range attempts {
		jobs <- struct{}{}
	}
	close(jobs)
	wg.Wait()

	s.Equal(0, counts[codes.OK], "no unknown token may be admitted: %v", counts)
	s.Positive(counts[codes.ResourceExhausted], "some calls of the burst should be limited: %v", counts)
	s.Positive(counts[codes.Unauthenticated], "some calls of the burst should be reviewed and refused: %v", counts)

	tok := kubectlCreateToken(t, ctx, r, managers.TestServiceAccount)
	s.Require().Eventually(func() bool {
		cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		si, err := s.arrive(cctx, mc, "mgrapi-after-flood", tok)
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
