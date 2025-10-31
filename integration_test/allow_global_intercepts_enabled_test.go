package integration_test

import (
	"github.com/telepresenceio/telepresence/v2/integration_test/itest"
)

// allowGlobalInterceptsEnabledSuite tests the default behavior when
// allowGlobalIntercepts is true (or not set).
type allowGlobalInterceptsEnabledSuite struct {
	itest.Suite
	itest.SingleService
}

func (s *allowGlobalInterceptsEnabledSuite) SuiteName() string {
	return "AllowGlobalInterceptsEnabled"
}

func (s *allowGlobalInterceptsEnabledSuite) SetupSuite() {
	s.Suite.SetupSuite()
	s.SingleService.SetupSuite()
	// Explicitly set allowGlobalIntercepts=true to test default behavior
	s.TelepresenceHelmInstallOK(s.Context(), true, "--set", "intercept.allowGlobalIntercepts=true")
}

// Test_GlobalInterceptWorks verifies that global TCP intercepts work when enabled.
func (s *allowGlobalInterceptsEnabledSuite) Test_GlobalInterceptWorks() {
	require := s.Require()
	ctx := s.Context()

	// Create a global TCP intercept without HTTP filters
	stdout, stderr, err := itest.Telepresence(ctx, "intercept", s.ServiceName(), "--port", "8080")
	require.NoError(err, "Global intercept should work when enabled - stderr: %s", stderr)
	require.Contains(stdout, "Using Deployment")

	// Clean up
	_, _, err = itest.Telepresence(ctx, "leave", s.ServiceName())
	require.NoError(err)
}

// Test_HTTPInterceptWorks verifies that HTTP intercepts work when global intercepts are enabled.
func (s *allowGlobalInterceptsEnabledSuite) Test_HTTPInterceptWorks() {
	require := s.Require()
	ctx := s.Context()

	// HTTP intercept should work
	stdout, stderr, err := itest.Telepresence(ctx, "intercept", s.ServiceName(),
		"--http-header", "X-User-ID=test123",
		"--port", "8080")
	require.NoError(err, "HTTP intercept should work - stderr: %s", stderr)
	require.Contains(stdout, "Using Deployment")

	// Clean up
	_, _, err = itest.Telepresence(ctx, "leave", s.ServiceName())
	require.NoError(err)
}

func init() {
	itest.AddSingleServiceSuite("", "echo", func(h itest.SingleService) itest.TestingSuite {
		return &allowGlobalInterceptsEnabledSuite{Suite: itest.Suite{Harness: h}, SingleService: h}
	})
}
