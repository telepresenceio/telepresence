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

func init() {
	itest.AddSingleServiceSuite("", "echo", func(h itest.SingleService) itest.TestingSuite {
		return &httpInterceptsSuite{Suite: itest.Suite{Harness: h}, SingleService: h}
	})
}
