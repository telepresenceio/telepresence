package auth

import (
	"context"
	"sync"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/managers"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
)

// MgrapiReviewBudgetPermissive proves that in permissive mode a call whose
// token could not be reviewed because the review limit was reached is
// refused, while a caller with a previously accepted token is still served.
type MgrapiReviewBudgetPermissive struct {
	rt.Suite
}

func init() {
	rt.Register(&MgrapiReviewBudgetPermissive{}, rt.InArea("auth"), rt.NeedsManager(managers.AuthPermissive()))
}

// Test_ThrottledTokenRefused floods the manager with distinct unknown tokens
// and probes with never-seen tokens meanwhile. Once the review budget is
// spent, a probe must be refused with ResourceExhausted, and a caller with an
// accepted token must still be served. A cluster too slow to exceed the
// budget skips the test.
func (s *MgrapiReviewBudgetPermissive) Test_ThrottledTokenRefused() {
	const (
		floodFor   = 20 * time.Second
		floodRate  = 60 // unknown-token calls per second; each costs two reviews against a 50/s budget
		workers    = 16
		probeEvery = 250 * time.Millisecond
	)
	t := s.T()
	ctx := s.Ctx()
	r := s.R()
	ns := s.AppNamespace()
	s.Manager()
	mc, closeFn, err := rt.ManagerClient(rt.Env{Ctx: ctx, T: t, R: r}, managers.ManagerNamespace)
	s.Require().NoError(err)
	defer closeFn()

	arrive := func(ctx context.Context, name, tok string) error {
		cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		tokCtx := metadata.AppendToOutgoingContext(cctx, "authorization", "Bearer "+tok)
		si, err := mc.ArriveAsClient(tokCtx, &manager.ClientInfo{
			Name:      name,
			Namespace: ns,
			InstallId: name,
			Product:   "telepresence",
			Version:   "v" + r.Version().String(),
		})
		if err == nil {
			_, _ = mc.Depart(tokCtx, si)
		}
		return err
	}

	known := kubectlCreateToken(t, ctx, r, managers.TestServiceAccount)
	s.Require().NoError(arrive(ctx, "mgrapi-known", known), "the known caller must be admitted before the flood")

	fctx, stop := context.WithTimeout(ctx, floodFor)
	defer stop()

	var (
		mu          sync.Mutex
		unknown     = map[codes.Code]int{}
		probeCounts = map[codes.Code]int{}
		knownCounts = map[codes.Code]int{}
		wg          sync.WaitGroup
	)
	start := time.Now()
	ticks := make(chan struct{})
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer close(ticks)
		ticker := time.NewTicker(time.Second / floodRate)
		defer ticker.Stop()
		for {
			select {
			case <-fctx.Done():
				return
			case <-ticker.C:
				select {
				case ticks <- struct{}{}:
				case <-fctx.Done():
					return
				}
			}
		}
	}()
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range ticks {
				err := arrive(fctx, "mgrapi-unknown", uuid.NewString())
				if fctx.Err() != nil {
					return
				}
				mu.Lock()
				unknown[status.Code(err)]++
				mu.Unlock()
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(probeEvery)
		defer ticker.Stop()
		for {
			select {
			case <-fctx.Done():
				return
			case <-ticker.C:
				perr := arrive(fctx, "mgrapi-probe", uuid.NewString())
				kerr := arrive(fctx, "mgrapi-known", known)
				if fctx.Err() != nil {
					return
				}
				mu.Lock()
				probeCounts[status.Code(perr)]++
				knownCounts[status.Code(kerr)]++
				mu.Unlock()
			}
		}
	}()
	wg.Wait()
	elapsed := time.Since(start)

	s.Zero(knownCounts[codes.ResourceExhausted], "the known caller must not be throttled: %v", knownCounts)
	s.Positive(knownCounts[codes.OK], "the known caller must have been served: %v", knownCounts)
	if probeCounts[codes.ResourceExhausted] == 0 {
		total := 0
		for _, n := range unknown {
			total += n
		}
		if float64(total) <= 100+50*elapsed.Seconds() {
			s.T().Skipf("only %d unknown-token calls in %s: too slow to exceed the review budget: %v", total, elapsed, unknown)
		}
		s.Failf("no never-seen token was refused", "unknown %v, probes %v", unknown, probeCounts)
	}
}
