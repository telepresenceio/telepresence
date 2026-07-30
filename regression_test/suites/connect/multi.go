package connect

import (
	"fmt"
	"time"

	"github.com/telepresenceio/telepresence/v2/regression_test/framework/cli"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/managers"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/workloads"
)

// attachTimeout bounds how long an intercept takes to show up in `list`.
const attachTimeout = 30 * time.Second

// workloadInfoJSON extends cli.ListEntry with the intercept/ingest presence
// fields `telepresence list --format json` emits (rpc/connector.WorkloadInfo,
// json tags intercept_info/ingest_info) that cli.ListEntry doesn't mirror;
// this suite needs them to tell an attached workload from a merely-listed
// one.
type workloadInfoJSON struct {
	cli.ListEntry
	InterceptInfo []map[string]any `json:"intercept_info,omitempty"`
	IngestInfo    []map[string]any `json:"ingest_info,omitempty"`
}

// ConnectMulti proves two simultaneous docker connections stay independent:
// "alpha" to rtest-app via the shared manager, "beta" to a private
// namespace via a SecondaryManager. Each lists only its own namespace's
// workloads, each can hold an intercept concurrently, and quitting one
// leaves the other connected.
//
// beta is driven through raw CLI calls rather than rt.ConnectionFixture:
// ConnectionFixture always passes --manager-namespace managers.
// ManagerNamespace (the shared manager), but a SecondaryManager's release
// lives in the namespace it manages (rt.SecondaryManager installs with
// `helm install -n <ns>`), so a connection to it needs a
// --manager-namespace ConnectionFixture has no option to express. connectAs
// (this package's reconstruction of rt's private identity constant) still
// applies unchanged: ensureManagerRBAC grants it against the shared
// manager namespace's ServiceAccount regardless of which manager release a
// connection targets.
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

	_, stderr, err := s.CLI().Run(ctx, "connect",
		"--namespace", privateNS, "--manager-namespace", privateNS,
		"--name", "beta", "--docker", "--as", connectAs)
	s.Require().NoError(err, "connect beta: %s", stderr)
	betaQuit := false
	t.Cleanup(func() {
		if !betaQuit {
			if _, _, err := s.CLI().Run(ctx, "quit", "--use", "beta"); err != nil {
				s.R().Infof("[rtest] ConnectMulti: quit --use beta: %v", err)
			}
		}
	})

	s.True(present(s.multiList("alpha"), wlAlpha.Name, wlAlpha.Namespace), "alpha should list its own workload")
	s.False(present(s.multiList("alpha"), wlBeta.Name, wlBeta.Namespace), "alpha should not see beta's namespace")
	s.True(present(s.multiList("beta"), wlBeta.Name, wlBeta.Namespace), "beta should list its own workload")
	s.False(present(s.multiList("beta"), wlAlpha.Name, wlAlpha.Namespace), "beta should not see alpha's namespace")

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
	_, stderr, err = s.CLI().Run(ctx, "intercept", wlBeta.Name, "--namespace", wlBeta.Namespace,
		"--port", fmt.Sprintf("%d:http", lsB.Port()), "--mount", "false", "--use", "beta")
	s.Require().NoError(err, "intercept beta: %s", stderr)
	defer func() {
		if _, _, err := s.CLI().Run(ctx, "detach", wlBeta.Name, "-n", wlBeta.Namespace, "--use", "beta"); err != nil {
			s.R().Infof("[rtest] ConnectMulti: detach beta: %v", err)
		}
	}()

	s.Eventually(func() bool {
		return attached(s.multiList("alpha"), wlAlpha.Name, wlAlpha.Namespace)
	}, attachTimeout, time.Second, "alpha's list did not show the intercept")
	s.Eventually(func() bool {
		return attached(s.multiList("beta"), wlBeta.Name, wlBeta.Namespace)
	}, attachTimeout, time.Second, "beta's list did not show the intercept")

	// Quit alpha; beta must remain connected and listable.
	alpha.Disconnect(t)
	alphaQuit = true
	alphaDetached = true
	s.True(present(s.multiList("beta"), wlBeta.Name, wlBeta.Namespace), "beta should still list after alpha quit")
}

// multiList runs `list --format json --use <use>` and parses the extended
// shape this suite needs (see workloadInfoJSON).
func (s *ConnectMulti) multiList(use string) []workloadInfoJSON {
	t := s.T()
	t.Helper()
	var entries []workloadInfoJSON
	if err := s.CLI().JSON(s.Ctx(), &entries, "list", "--format", "json", "--use", use); err != nil {
		t.Fatalf("list --use %s: %v", use, err)
	}
	return entries
}

// present reports whether entries contains a workload named name in ns.
func present(entries []workloadInfoJSON, name, ns string) bool {
	for _, e := range entries {
		if e.Name == name && e.Namespace == ns {
			return true
		}
	}
	return false
}

// attached reports whether the workload named name in ns is currently
// intercepted or ingested.
func attached(entries []workloadInfoJSON, name, ns string) bool {
	for _, e := range entries {
		if e.Name == name && e.Namespace == ns {
			return len(e.InterceptInfo)+len(e.IngestInfo) > 0
		}
	}
	return false
}
