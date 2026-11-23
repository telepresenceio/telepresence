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

// MgrapiReviewBudget proves that a flood of unknown bearer tokens does not
// lock out a caller whose token the traffic-manager accepted before the
// flood, including after that caller's cached verdict has expired.
type MgrapiReviewBudget struct {
	rt.Suite
}

func init() {
	rt.Register(&MgrapiReviewBudget{}, rt.InArea("auth"), rt.NeedsManager(managers.AuthEnforcing()), rt.WithLabels(rt.Slow))
}

// Test_KnownTokenServedDuringUnknownTokenFlood floods the manager with
// distinct unknown tokens for longer than the manager caches a good verdict
// (two minutes) while a caller with an accepted token keeps calling. None of
// that caller's calls may be answered with ResourceExhausted, and the flood
// itself must have been throttled. A cluster too slow to exceed the review
// budget skips the test.
func (s *MgrapiReviewBudget) Test_KnownTokenServedDuringUnknownTokenFlood() {
	const (
		floodFor   = 3 * time.Minute // longer than the 2 minute success cache TTL
		floodRate  = 60              // unknown-token calls per second; each costs two reviews against a 50/s budget
		workers    = 16
		knownEvery = 2 * time.Second
		budgetQS   = 50
		burst      = 100
	)
	t := s.T()
	ctx := s.Ctx()
	r := s.R()
	ns := s.AppNamespace()
	s.Manager()
	mc, closeFn, err := rt.ManagerClient(rt.Env{Ctx: ctx, T: t, R: r}, managers.ManagerNamespace)
	s.Require().NoError(err)
	defer closeFn()

	arrive := func(ctx context.Context, name, tok string) (*manager.SessionInfo, error) {
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
		return si, err
	}

	known := kubectlCreateToken(t, ctx, r, managers.TestServiceAccount)
	_, err = arrive(ctx, "mgrapi-known", known)
	s.Require().NoError(err, "the known caller must be admitted before the flood")

	fctx, stop := context.WithTimeout(ctx, floodFor)
	defer stop()

	var (
		mu          sync.Mutex
		unknown     = map[codes.Code]int{}
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
				_, err := arrive(fctx, "mgrapi-unknown", uuid.NewString())
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
		ticker := time.NewTicker(knownEvery)
		defer ticker.Stop()
		for {
			select {
			case <-fctx.Done():
				return
			case <-ticker.C:
				_, err := arrive(fctx, "mgrapi-known", known)
				if fctx.Err() != nil {
					return
				}
				mu.Lock()
				knownCounts[status.Code(err)]++
				mu.Unlock()
			}
		}
	}()
	wg.Wait()
	elapsed := time.Since(start)

	s.Zero(knownCounts[codes.ResourceExhausted], "the known caller must not be throttled: %v", knownCounts)
	s.Positive(knownCounts[codes.OK], "the known caller must have been served: %v", knownCounts)
	s.Zero(unknown[codes.OK], "no unknown token may be admitted: %v", unknown)

	total := 0
	for _, n := range unknown {
		total += n
	}
	if unknown[codes.ResourceExhausted] == 0 {
		if float64(total) <= burst+budgetQS*elapsed.Seconds() {
			s.T().Skipf("only %d unknown-token calls in %s: too slow to exceed the review budget: %v", total, elapsed, unknown)
		}
		s.Failf("no unknown-token call was limited", "%d calls in %s, budget %d + %d/s: %v", total, elapsed, burst, budgetQS, unknown)
	}
}
