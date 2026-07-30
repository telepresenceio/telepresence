// Package regression_test is the entry point for the regression-test
// binary: it wires up rt.Main and the per-area test functions. Suites
// register themselves via blank imports.
package regression_test

import (
	"testing"

	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"

	_ "github.com/telepresenceio/telepresence/v2/regression_test/suites/attach"
	_ "github.com/telepresenceio/telepresence/v2/regression_test/suites/connect"
	_ "github.com/telepresenceio/telepresence/v2/regression_test/suites/injector"
	_ "github.com/telepresenceio/telepresence/v2/regression_test/suites/install"
	_ "github.com/telepresenceio/telepresence/v2/regression_test/suites/intercept"
	_ "github.com/telepresenceio/telepresence/v2/regression_test/suites/namespaces"
	_ "github.com/telepresenceio/telepresence/v2/regression_test/suites/smoke"
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
