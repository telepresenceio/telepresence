package connect

import (
	"time"

	"github.com/telepresenceio/telepresence/v2/regression_test/framework/cli"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/managers"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/workloads"
)

// attachTimeout bounds how long an intercept takes to show up in `list`.
const attachTimeout = 30 * time.Second

// ConnectMulti proves two simultaneous docker connections stay independent:
// "alpha" to rtest-app via the shared manager, "beta" to a private
// namespace via a SecondaryManager. Each lists only its own namespace's
// workloads, each can hold an intercept concurrently, and quitting one
// leaves the other connected.
type ConnectMulti struct {
	rt.Suite
}

func init() {
	rt.Register(&ConnectMulti{},
		rt.InArea("connect"),
		rt.NeedsManager(managers.Default),
		rt.Requires(rt.Docker),
		rt.On("linux"),
	)
}

func (s *ConnectMulti) Test_TwoDockerConnections() {
	t := s.T()
	ctx := s.Ctx()
	s.Manager()

	env := rt.Env{Ctx: ctx, T: t, R: s.R()}
	// Unmanaged: the shared manager's namespaceSelector matches the
	// rtest.telepresence.io/managed label PrivateNamespace applies, which
	// would make it "already managed" once the SecondaryManager below also
	// claims it.
	privateNS := rt.PrivateUnmanagedNamespace(env, "multi-beta")
	rt.Get(t, rt.SecondaryManager(managers.Default, privateNS))

	wlAlpha := s.Workload(workloads.Echo("multi-alpha"))
	wlBeta := rt.Get(t, rt.WorkloadFixture(privateNS, workloads.Echo("multi-beta")))

	alpha := s.Connect(rt.ConnNamed("alpha"), rt.ConnDocker())
	alphaQuit := false
	t.Cleanup(func() {
		if !alphaQuit {
			alpha.Disconnect(t)
		}
	})

	// beta's release lives in the namespace it manages (rt.SecondaryManager
	// installs with `helm install -n <ns>`), not the shared manager's
	// namespace, so it needs rt.ConnManagerNamespace: the shared Connect
	// path (Suite.Connect/ConnectionFixture's default) always targets
	// managers.ManagerNamespace.
	beta := rt.Get(t, rt.ConnectionFixture(privateNS,
		rt.ConnNamed("beta"), rt.ConnDocker(), rt.ConnManagerNamespace(privateNS)))
	betaQuit := false
	t.Cleanup(func() {
		if !betaQuit {
			beta.Disconnect(t)
		}
	})

	s.True(present(alpha.List(t), wlAlpha.Name, wlAlpha.Namespace), "alpha should list its own workload")
	s.False(present(alpha.List(t), wlBeta.Name, wlBeta.Namespace), "alpha should not see beta's namespace")
	s.True(present(beta.List(t), wlBeta.Name, wlBeta.Namespace), "beta should list its own workload")
	s.False(present(beta.List(t), wlAlpha.Name, wlAlpha.Namespace), "beta should not see alpha's namespace")

	lsA := s.LocalEcho()
	aA := alpha.Intercept(t, wlAlpha, rt.ToLocal(lsA, "http"), cli.MountFalse())
	alphaDetached := false
	defer func() {
		// Quitting alpha removes its session and intercepts with it; a
		// detach after the quit would implicitly connect to "default".
		if !alphaDetached {
			aA.Detach(t)
		}
	}()

	lsB := s.LocalEcho()
	aB := beta.Intercept(t, wlBeta, rt.ToLocal(lsB, "http"), cli.MountFalse())
	defer aB.Detach(t)

	s.Eventually(func() bool {
		return attached(alpha.List(t), wlAlpha.Name, wlAlpha.Namespace)
	}, attachTimeout, time.Second, "alpha's list did not show the intercept")
	s.Eventually(func() bool {
		return attached(beta.List(t), wlBeta.Name, wlBeta.Namespace)
	}, attachTimeout, time.Second, "beta's list did not show the intercept")

	// Quit alpha; beta must remain connected and listable.
	alpha.Disconnect(t)
	alphaQuit = true
	alphaDetached = true
	s.True(present(beta.List(t), wlBeta.Name, wlBeta.Namespace), "beta should still list after alpha quit")
}

// present reports whether entries contains a workload named name in ns.
func present(entries []cli.ListEntry, name, ns string) bool {
	for _, e := range entries {
		if e.Name == name && e.Namespace == ns {
			return true
		}
	}
	return false
}

// attached reports whether the workload named name in ns is currently
// intercepted or ingested.
func attached(entries []cli.ListEntry, name, ns string) bool {
	for _, e := range entries {
		if e.Name == name && e.Namespace == ns {
			return len(e.InterceptInfo)+len(e.IngestInfo) > 0
		}
	}
	return false
}
