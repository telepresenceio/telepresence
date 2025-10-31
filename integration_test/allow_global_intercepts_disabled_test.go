package integration_test

import (
	"github.com/telepresenceio/telepresence/v2/integration_test/itest"
)

// allowGlobalInterceptsDisabledSuite tests the behavior when
// allowGlobalIntercepts is false.
type allowGlobalInterceptsDisabledSuite struct {
	itest.Suite
	itest.SingleService
}

func (s *allowGlobalInterceptsDisabledSuite) SuiteName() string {
	return "AllowGlobalInterceptsDisabled"
}

func (s *allowGlobalInterceptsDisabledSuite) SetupSuite() {
	s.Suite.SetupSuite()
	s.SingleService.SetupSuite()
	// Configure traffic manager to disable global intercepts
	s.TelepresenceHelmInstallOK(s.Context(), true, "--set", "intercept.allowGlobalIntercepts=false")
}

// Test_GlobalInterceptBlocked verifies that global TCP intercepts are blocked when disabled
func (s *allowGlobalInterceptsDisabledSuite) Test_GlobalInterceptBlocked() {
	require := s.Require()
	ctx := s.Context()

	// Attempt to create a global TCP intercept without HTTP filters
	_, stderr, err := itest.Telepresence(ctx, "intercept", s.ServiceName(), "--port", "8080")

	// Should fail with appropriate error
	require.Error(err, "Global intercept should be blocked when allowGlobalIntercepts=false")
	require.Contains(stderr, "global TCP/UDP intercepts are disabled",
		"Error should explain that global intercepts are disabled")
	require.Contains(stderr, "--http-header",
		"Error should suggest using --http-header flag")
}

// Test_HTTPInterceptWithHeaderWorks verifies that HTTP intercepts work when global intercepts are disabled
func (s *allowGlobalInterceptsDisabledSuite) Test_HTTPInterceptWithHeaderWorks() {
	require := s.Require()
	ctx := s.Context()

	// HTTP intercept with header should work
	stdout, stderr, err := itest.Telepresence(ctx, "intercept", s.ServiceName(),
		"--http-header", "X-User-ID=dev123",
		"--port", "8080")
	require.NoError(err, "HTTP intercept with headers should work - stderr: %s", stderr)
	require.Contains(stdout, "Using Deployment")

	// Clean up
	_, _, err = itest.Telepresence(ctx, "leave", s.ServiceName())
	require.NoError(err)
}

// Test_HTTPInterceptWithPathWorks verifies that HTTP intercepts with paths work when global intercepts are disabled
func (s *allowGlobalInterceptsDisabledSuite) Test_HTTPInterceptWithPathWorks() {
	require := s.Require()
	ctx := s.Context()

	// HTTP intercept with path should work
	stdout, stderr, err := itest.Telepresence(ctx, "intercept", s.ServiceName(),
		"--http-path-prefix", "/api/",
		"--port", "8080")
	require.NoError(err, "HTTP intercept with path should work - stderr: %s", stderr)
	require.Contains(stdout, "Using Deployment")

	// Clean up
	_, _, err = itest.Telepresence(ctx, "leave", s.ServiceName())
	require.NoError(err)
}

// Test_HTTPInterceptWithCombinedFiltersWorks verifies that HTTP intercepts with both headers and paths work
func (s *allowGlobalInterceptsDisabledSuite) Test_HTTPInterceptWithCombinedFiltersWorks() {
	require := s.Require()
	ctx := s.Context()

	// HTTP intercept with both filters should work
	stdout, stderr, err := itest.Telepresence(ctx, "intercept", s.ServiceName(),
		"--http-header", "X-User-ID=test456",
		"--http-path-prefix", "/api/",
		"--port", "8080")
	require.NoError(err, "HTTP intercept with combined filters should work - stderr: %s", stderr)
	require.Contains(stdout, "Using Deployment")

	// Clean up
	_, _, err = itest.Telepresence(ctx, "leave", s.ServiceName())
	require.NoError(err)
}

// Test_MultipleHTTPInterceptsCoexist verifies that multiple HTTP intercepts can coexist
func (s *allowGlobalInterceptsDisabledSuite) Test_MultipleHTTPInterceptsCoexist() {
	require := s.Require()
	ctx := s.Context()

	// Start first HTTP intercept
	stdout1, stderr1, err1 := itest.Telepresence(ctx, "intercept", "http-one",
		"--workload", s.ServiceName(),
		"--http-header", "X-User=alice",
		"--port", "8080:80",
		"--mount", "false")
	require.NoError(err1, "First HTTP intercept should succeed - stderr: %s", stderr1)
	require.Contains(stdout1, "Using Deployment")

	// Start second HTTP intercept with different header
	stdout2, stderr2, err2 := itest.Telepresence(ctx, "intercept", "http-two",
		"--workload", s.ServiceName(),
		"--http-header", "X-User=bob",
		"--port", "8081:80",
		"--mount", "false")
	require.NoError(err2, "Second HTTP intercept should succeed - stderr: %s", stderr2)
	require.Contains(stdout2, "Using Deployment")

	// Verify both intercepts are active
	listOutput, listStderr, listErr := itest.Telepresence(ctx, "list", "--intercepts")
	require.NoError(listErr, "Failed to list intercepts - stderr: %s", listStderr)
	require.Contains(listOutput, "Intercept name: http-one")
	require.Contains(listOutput, "Intercept name: http-two")

	// Clean up both intercepts
	_, _, err3 := itest.Telepresence(ctx, "leave", "http-one")
	require.NoError(err3)

	_, _, err4 := itest.Telepresence(ctx, "leave", "http-two")
	require.NoError(err4)
}

func init() {
	itest.AddSingleServiceSuite("", "echo", func(h itest.SingleService) itest.TestingSuite {
		return &allowGlobalInterceptsDisabledSuite{Suite: itest.Suite{Harness: h}, SingleService: h}
	})
}
