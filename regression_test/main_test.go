// Package regression_test is the entry point for the regression-test
// binary: it wires up rt.Main and the per-area test functions. Suites
// register themselves via blank imports.
package regression_test

import (
	"testing"

	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"

	_ "github.com/telepresenceio/telepresence/v2/regression_test/suites/attach"
	_ "github.com/telepresenceio/telepresence/v2/regression_test/suites/auth"
	_ "github.com/telepresenceio/telepresence/v2/regression_test/suites/connect"
	_ "github.com/telepresenceio/telepresence/v2/regression_test/suites/dns"
	_ "github.com/telepresenceio/telepresence/v2/regression_test/suites/docker"
	_ "github.com/telepresenceio/telepresence/v2/regression_test/suites/injector"
	_ "github.com/telepresenceio/telepresence/v2/regression_test/suites/install"
	_ "github.com/telepresenceio/telepresence/v2/regression_test/suites/intercept"
	_ "github.com/telepresenceio/telepresence/v2/regression_test/suites/mounts"
	_ "github.com/telepresenceio/telepresence/v2/regression_test/suites/namespaces"
	_ "github.com/telepresenceio/telepresence/v2/regression_test/suites/nodeagent"
	_ "github.com/telepresenceio/telepresence/v2/regression_test/suites/quic"
	_ "github.com/telepresenceio/telepresence/v2/regression_test/suites/routing"
	_ "github.com/telepresenceio/telepresence/v2/regression_test/suites/session"
	_ "github.com/telepresenceio/telepresence/v2/regression_test/suites/smoke"
	_ "github.com/telepresenceio/telepresence/v2/regression_test/suites/state"
)

func TestMain(m *testing.M) {
	rt.Main(m)
}

// TestSmoke runs the "smoke" area. It is declared first: smoke suites
// assume no prior suite has left a connection behind.
func TestSmoke(t *testing.T) {
	rt.RunArea(t, "smoke")
}

// TestConnect runs the "connect" area. It runs before the areas that hold a
// long-lived shared connection: its suites churn connections (quit, named,
// docker) and would otherwise invalidate theirs.
func TestConnect(t *testing.T) {
	rt.RunArea(t, "connect")
}

// TestAttach runs the "attach" area.
func TestAttach(t *testing.T) {
	rt.RunArea(t, "attach")
}

// TestIntercept runs the "intercept" area.
func TestIntercept(t *testing.T) {
	rt.RunArea(t, "intercept")
}

// TestInstall runs the "install" area. It churns the shared release's spec
// and installs secondary releases; it runs after the data-plane areas.
func TestInstall(t *testing.T) {
	rt.RunArea(t, "install")
}

// TestInjector runs the "injector" area.
func TestInjector(t *testing.T) {
	rt.RunArea(t, "injector")
}

// TestNamespaces runs the "namespaces" area.
func TestNamespaces(t *testing.T) {
	rt.RunArea(t, "namespaces")
}

// TestDns runs the "dns" area.
func TestDns(t *testing.T) {
	rt.RunArea(t, "dns")
}

// TestRouting runs the "routing" area.
func TestRouting(t *testing.T) {
	rt.RunArea(t, "routing")
}

// TestMounts runs the "mounts" area.
func TestMounts(t *testing.T) {
	rt.RunArea(t, "mounts")
}

// TestDocker runs the "docker" area.
func TestDocker(t *testing.T) {
	rt.RunArea(t, "docker")
}

// TestSession runs the "session" area.
func TestSession(t *testing.T) {
	rt.RunArea(t, "session")
}

// TestNodeAgent runs the "nodeagent" area.
func TestNodeAgent(t *testing.T) {
	rt.RunArea(t, "nodeagent")
}

// TestAuth runs the "auth" area.
func TestAuth(t *testing.T) {
	rt.RunArea(t, "auth")
}

// TestState runs the "state" area. Its apply/delete manifests own their
// connection lifecycle and quit every daemon between tests, so it runs
// after the areas that hold a long-lived shared connection; quic follows
// because its suites reconnect per suite anyway.
func TestState(t *testing.T) {
	rt.RunArea(t, "state")
}

// TestQuic runs the "quic" area. It runs last: transport experiments are
// the most invasive spec churn.
func TestQuic(t *testing.T) {
	rt.RunArea(t, "quic")
}
