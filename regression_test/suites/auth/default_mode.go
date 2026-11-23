package auth

import (
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/managers"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
)

// AuthDefaultMode proves that a traffic-manager installed without
// security.authentication.mode enforces authentication.
type AuthDefaultMode struct {
	rt.Suite
}

func init() {
	rt.Register(&AuthDefaultMode{}, rt.InArea("auth"), rt.NeedsManager(managers.Default))
}

// Test_UnsetModeRefusesCallWithoutToken sends ArriveAsClient to a release
// whose values do not set the mode. The call carries no token and must be
// refused with Unauthenticated.
func (s *AuthDefaultMode) Test_UnsetModeRefusesCallWithoutToken() {
	t := s.T()
	ctx := s.Ctx()
	r := s.R()
	ns := s.AppNamespace()
	s.Manager()

	s.Require().Empty(managers.Default.Values.Security.Authentication.Mode, "the Default spec must leave the mode unset")

	mode, err := r.Kubectl(ctx, managers.ManagerNamespace, "get", "statefulset", "traffic-manager", "-o",
		`jsonpath={.spec.template.spec.containers[*].env[?(@.name=="AUTHENTICATION_MODE")].value}`)
	s.Require().NoError(err)
	s.Equal("enforcing", strings.TrimSpace(mode), "the chart default must render an enforcing traffic-manager")

	mc, closeFn, err := rt.ManagerClient(rt.Env{Ctx: ctx, T: t, R: r}, managers.ManagerNamespace)
	s.Require().NoError(err)
	defer closeFn()

	_, err = mc.ArriveAsClient(ctx, &manager.ClientInfo{
		Name:      "rtest-auth-default-mode",
		Namespace: ns,
		InstallId: "rtest-auth-default-mode",
		Product:   "telepresence",
		Version:   "v" + r.Version().String(),
	})
	s.Require().Error(err, "a call without a token must be refused when the mode is unset")
	s.Equal(codes.Unauthenticated, status.Code(err), "ArriveAsClient without a token: %v", err)
}
