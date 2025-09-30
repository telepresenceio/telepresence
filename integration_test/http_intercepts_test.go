package integration_test

import (
	"github.com/telepresenceio/telepresence/v2/integration_test/itest"
)

type httpInterceptsSuite struct {
	itest.Suite
	itest.SingleService
}

func (s *httpInterceptsSuite) SuiteName() string {
	return "HTTPIntercepts"
}

func (s *httpInterceptsSuite) Test_HTTPHeaderFiltering() {
	require := s.Require()
	ctx := s.Context()

	// Test HTTP intercept with header filters using equals format
	stdout, stderr, err := itest.Telepresence(ctx, "intercept", s.ServiceName(), "--http-header", "X-User-ID=dev123", "--port", "8080")
	require.NoError(err, "stderr: %s", stderr)
	require.Contains(stdout, "Using Deployment")

	// Clean up
	_, _, err = itest.Telepresence(ctx, "leave", s.ServiceName())
	require.NoError(err)
}

func (s *httpInterceptsSuite) Test_HTTPHeaderFiltering_CurlFormat() {
	require := s.Require()
	ctx := s.Context()

	// Test HTTP intercept with header filters using colon format (curl -H compatible)
	stdout, stderr, err := itest.Telepresence(ctx, "intercept", s.ServiceName(), "--http-header", "X-User-ID: dev123", "--port", "8080")
	require.NoError(err, "stderr: %s", stderr)
	require.Contains(stdout, "Using Deployment")

	// Clean up
	_, _, err = itest.Telepresence(ctx, "leave", s.ServiceName())
	require.NoError(err)
}

func (s *httpInterceptsSuite) Test_HTTPPathFiltering() {
	require := s.Require()
	ctx := s.Context()

	// Test HTTP intercept with path filters
	stdout, stderr, err := itest.Telepresence(ctx, "intercept", s.ServiceName(), "--http-path-prefix", "/api/", "--port", "8080")
	require.NoError(err, "stderr: %s", stderr)
	require.Contains(stdout, "Using Deployment")

	// Clean up
	_, _, err = itest.Telepresence(ctx, "leave", s.ServiceName())
	require.NoError(err)
}

func (s *httpInterceptsSuite) Test_HTTPCombinedFiltering() {
	require := s.Require()
	ctx := s.Context()

	// Test HTTP intercept with both header and path filters, using mixed formats
	stdout, stderr, err := itest.Telepresence(ctx, "intercept", s.ServiceName(),
		"--http-header", "X-User-ID=dev123",
		"--http-header", "Authorization: Bearer token123",
		"--http-path-prefix", "/api/",
		"--port", "8080")
	require.NoError(err, "stderr: %s", stderr)
	require.Contains(stdout, "Using Deployment")

	// Clean up
	_, _, err = itest.Telepresence(ctx, "leave", s.ServiceName())
	require.NoError(err)
}

func (s *httpInterceptsSuite) Test_BackwardCompatibility() {
	require := s.Require()
	ctx := s.Context()

	// Test that standard TCP intercepts still work without HTTP filters
	stdout, stderr, err := itest.Telepresence(ctx, "intercept", s.ServiceName(), "--port", "8080")
	require.NoError(err, "stderr: %s", stderr)
	require.Contains(stdout, "Using Deployment")

	// Clean up
	_, _, err = itest.Telepresence(ctx, "leave", s.ServiceName())
	require.NoError(err)
}

func (s *httpInterceptsSuite) Test_HTTPInterceptCoexistence() {
	require := s.Require()
	ctx := s.Context()

	// This test verifies that multiple personal intercepts with different HTTP headers
	// can coexist on the same workload without conflicts (fixes issue #3969)

	// Start first personal intercept with x-user=adam
	stdout1, stderr1, err1 := itest.Telepresence(ctx, "intercept", "echo-one",
		"--workload", s.ServiceName(),
		"--http-header", "x-user=adam",
		"--port", "8080:80",
		"--mount", "false")
	require.NoError(err1, "First intercept failed - stderr: %s", stderr1)
	require.Contains(stdout1, "Using Deployment")

	// Start second personal intercept with x-user=bertil
	stdout2, stderr2, err2 := itest.Telepresence(ctx, "intercept", "echo-two",
		"--workload", s.ServiceName(),
		"--http-header", "x-user=bertil",
		"--port", "8081:80",
		"--mount", "false")
	require.NoError(err2, "Second intercept failed - stderr: %s", stderr2)
	require.Contains(stdout2, "Using Deployment")

	// Verify both intercepts are active and listed
	listOutput, listStderr, listErr := itest.Telepresence(ctx, "list", "--intercepts")
	require.NoError(listErr, "Failed to list intercepts - stderr: %s", listStderr)
	require.Contains(listOutput, "echo-one: intercepted")
	require.Contains(listOutput, "echo-two: intercepted")

	// Clean up both intercepts
	_, _, err3 := itest.Telepresence(ctx, "leave", "echo-one")
	require.NoError(err3, "Failed to leave first intercept")

	_, _, err4 := itest.Telepresence(ctx, "leave", "echo-two")
	require.NoError(err4, "Failed to leave second intercept")
}

func (s *httpInterceptsSuite) Test_TCPPortConflictDetection() {
	require := s.Require()
	ctx := s.Context()

	// This test verifies that real TCP port conflicts are still properly detected
	// when two intercepts try to use the same local port

	// Start first intercept using port 8080
	stdout1, stderr1, err1 := itest.Telepresence(ctx, "intercept", "tcp-conflict-one",
		"--workload", s.ServiceName(),
		"--http-header", "x-user=adam",
		"--port", "8080:80",
		"--mount", "false")
	require.NoError(err1, "First intercept should succeed - stderr: %s", stderr1)
	require.Contains(stdout1, "Using Deployment")

	// Attempt second intercept using the SAME local port 8080
	// This should fail with a TCP port conflict, not an agent intercept conflict
	_, stderr2, err2 := itest.Telepresence(ctx, "intercept", "tcp-conflict-two",
		"--workload", s.ServiceName(),
		"--http-header", "x-user=bertil",
		"--port", "8080:80", // Same local port as first intercept
		"--mount", "false")

	// Should fail due to real TCP port conflict
	require.Error(err2, "Second intercept should fail due to TCP port conflict")

	// Verify it's a TCP port binding error, not an agent intercept conflict
	require.Contains(stderr2, "127.0.0.1:8080", "Error should mention the conflicting local port")
	require.Contains(stderr2, "already in use", "Error should indicate port is already in use")

	// Should NOT contain agent intercept conflict message
	require.NotContains(stderr2, "Conflicts with", "Should not be an agent intercept conflict")

	// Clean up the successful intercept
	_, _, err3 := itest.Telepresence(ctx, "leave", "tcp-conflict-one")
	require.NoError(err3, "Failed to leave first intercept")
}

func init() {
	itest.AddSingleServiceSuite("", "echo", func(h itest.SingleService) itest.TestingSuite {
		return &httpInterceptsSuite{Suite: itest.Suite{Harness: h}, SingleService: h}
	})
}
