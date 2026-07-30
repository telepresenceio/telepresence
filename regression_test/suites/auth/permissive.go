package auth

import (
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/cli"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/managers"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/workloads"
)

// AuthPermissive proves that security.authentication.mode=permissive (the
// chart's own default, set explicitly here so the spec is named for what it
// tests) leaves the standard test-developer connect/intercept flow
// unaffected: permissive mode validates a bearer token when one is present,
// but never rejects a call for lacking one.
type AuthPermissive struct {
	rt.Suite
}

func init() {
	rt.Register(&AuthPermissive{}, rt.InArea("auth"), rt.NeedsManager(managers.AuthPermissive()))
}

// Test_ConnectAndInterceptWork drives a plain connect -> intercept -> detach
// cycle against a permissive-mode manager, under the framework's standard
// test-developer identity.
func (s *AuthPermissive) Test_ConnectAndInterceptWork() {
	t := s.T()
	ctx := s.Ctx()
	ns := s.AppNamespace()
	conn := switchManagerSpec(t, ctx, managers.AuthPermissive(), ns)

	wl := s.Workload(workloads.Echo("auth-permissive"))
	ls := s.LocalEcho()
	a := conn.Intercept(t, wl, rt.ToLocal(ls, "http"), cli.MountFalse())
	defer a.Detach(t)
	rt.RoutedToLocal(t, wl.ServiceURL(), ls)
}
