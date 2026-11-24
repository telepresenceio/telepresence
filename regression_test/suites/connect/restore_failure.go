package connect

import (
	"context"
	"time"

	"github.com/telepresenceio/telepresence/v2/regression_test/framework/cli"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/managers"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
)

// restoreFailureTimeout bounds how long the client takes to give up on a
// session that the restarted traffic-manager refuses to restore.
const restoreFailureTimeout = 3 * time.Minute

// RestoreFailure proves that a session the traffic-manager cannot restore
// does not stay up as a dead session with routes and DNS in place.
type RestoreFailure struct {
	rt.Suite
}

func init() {
	rt.Register(&RestoreFailure{},
		rt.InArea("connect"),
		rt.NeedsManager(managers.Default),
		rt.Requires(rt.Sudo),
		rt.On("linux"),
		rt.WithLabels(rt.Slow),
	)
}

// Test_SessionEndsWhenNamespaceBecomesUnmanaged connects to a namespace of a
// manager with a static namespace list, then replaces the manager with one
// that no longer lists the namespace. ReconnectClient answers that the
// namespace is not managed, which cannot change by waiting. The client must
// report that it is not connected, the root daemon must remove the policy
// rules and routes of the session, and a connect to a managed namespace must
// work.
func (s *RestoreFailure) Test_SessionEndsWhenNamespaceBecomesUnmanaged() {
	t := s.T()
	ctx := s.Ctx()
	r := s.R()
	s.Manager()

	env := rt.Env{Ctx: ctx, T: t, R: r}
	nsA := rt.PrivateUnmanagedNamespace(env, "restore-a")
	nsB := rt.PrivateUnmanagedNamespace(env, "restore-b")

	freeDefaultConnection(t, s.AppNamespace())
	t.Cleanup(func() {
		if _, _, err := r.CLI().Run(context.WithoutCancel(ctx), "quit", "-s"); err != nil {
			r.Infof("[rtest] RestoreFailure: quit -s: %v", err)
		}
		r.ForgetConnections()
	})
	rt.Mutate(t, rt.ManagerFixture(managers.StaticNamespaces(nsA, nsB)))

	before := snapshotRouting(ctx)
	connA := rt.Mutate(t, rt.ConnectionFixture(nsA))
	s.Require().Equal(nsA, connA.Status(t).UserDaemon.Namespace)

	rules, tables := before.newRules(ctx)
	s.Require().NotEmpty(rules, "the connected root daemon must have created policy rules")
	t.Cleanup(func() {
		cctx := context.WithoutCancel(ctx)
		if left := before.leftoverRouting(cctx, tables...); left != "" {
			t.Logf("removing leftover routing state:\n%s", left)
			before.removeLeftoverRouting(cctx, tables...)
		}
	})

	// The replaced manager no longer lists nsA, so it refuses to restore the session.
	rt.Mutate(t, rt.ManagerFixture(managers.StaticNamespaces(nsB)))

	s.Require().Eventually(func() bool {
		var st cli.Status
		if err := s.CLI().JSON(ctx, &st, "status", "--format", "json"); err != nil {
			return false
		}
		return st.UserDaemon.Running && st.UserDaemon.Status == "Not connected"
	}, restoreFailureTimeout, 2*time.Second, "the client should report that it is not connected once the manager refuses to restore the session")

	s.Require().Eventually(func() bool { return before.leftoverRouting(ctx, tables...) == "" }, networkDownTimeout, pollInterval,
		"policy rules or routes are left after the session ended:\n%s", before.leftoverRouting(ctx, tables...))

	connB := rt.Mutate(t, rt.ConnectionFixture(nsB))
	s.Equal(nsB, connB.Status(t).UserDaemon.Namespace, "a connect to a managed namespace should work")
}
