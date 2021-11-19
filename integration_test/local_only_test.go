package integration_test

import (
	"time"

	"github.com/telepresenceio/telepresence/v2/integration_test/itest"
)

func (s *singleServiceSuite) Test_LocalOnlyIntercept() {
	ctx := s.Context()
	require := s.Require()

	stdout := itest.TelepresenceOk(ctx, "intercept", "--namespace", s.AppNamespace(), "--local-only", "mylocal")
	require.Empty(stdout)

	stdout = itest.TelepresenceOk(ctx, "list", "--namespace", s.AppNamespace(), "--intercepts")
	s.Contains(stdout, "mylocal: local-only intercept", "local intercept is not included in list output")

	// service can be resolve with unqualified name
	s.Eventually(func() bool {
		return itest.Run(ctx, "curl", "--silent", "--max-time", "2", s.ServiceName()) == nil
	}, 30*time.Second, 3*time.Second, "service is not reachable using unqualified name")

	stdout = itest.TelepresenceOk(ctx, "leave", "mylocal")
	s.Empty(stdout)
	s.Eventually(func() bool {
		return itest.Run(ctx, "curl", "--silent", "--max-time", "2", s.ServiceName()) != nil
	}, 30*time.Second, 3*time.Second, "leaving does not render services unavailable using unqualified name")
}
