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

	// Test CLI validation - headers without --http should fail
	_, stderr, err := itest.Telepresence(ctx, "intercept", s.ServiceName(), "--header", "X-User-ID=dev123", "--port", "8080")
	require.Error(err)
	require.Contains(stderr, "--header filters require --http flag")

	// Test valid HTTP intercept with headers
	stdout, stderr, err := itest.Telepresence(ctx, "intercept", s.ServiceName(), "--http", "--header", "X-User-ID=dev123", "--port", "8080")
	require.NoError(err, "stderr: %s", stderr)
	require.Contains(stdout, "Using Deployment")

	// Clean up
	_, _, err = itest.Telepresence(ctx, "leave", s.ServiceName())
	require.NoError(err)
}

func (s *httpInterceptsSuite) Test_HTTPPathFiltering() {
	require := s.Require()
	ctx := s.Context()

	// Test CLI validation - paths without --http should fail
	_, stderr, err := itest.Telepresence(ctx, "intercept", s.ServiceName(), "--path", "/api/*", "--port", "8080")
	require.Error(err)
	require.Contains(stderr, "--path filters require --http flag")

	// Test valid HTTP intercept with paths
	stdout, stderr, err := itest.Telepresence(ctx, "intercept", s.ServiceName(), "--http", "--path", "/api/*", "--port", "8080")
	require.NoError(err, "stderr: %s", stderr)
	require.Contains(stdout, "Using Deployment")

	// Clean up
	_, _, err = itest.Telepresence(ctx, "leave", s.ServiceName())
	require.NoError(err)
}

func (s *httpInterceptsSuite) Test_BackwardCompatibility() {
	require := s.Require()
	ctx := s.Context()

	// Test that standard TCP intercepts still work without --http
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
