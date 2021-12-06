package integration_test

import (
	"context"
	"fmt"

	"github.com/stretchr/testify/suite"

	"github.com/telepresenceio/telepresence/v2/integration_test/itest"
)

type cliSuite struct {
	itest.Suite
}

func init() {
	itest.AddClusterSuite(func(ctx context.Context) suite.TestingSuite {
		return &cliSuite{Suite: itest.Suite{Harness: itest.NewContextHarness(ctx)}}
	})
}

func (s *cliSuite) Test_Version() {
	stdout, stderr, err := itest.Telepresence(s.Context(), "version")
	if err != nil {
		s.SetGeneralError(fmt.Errorf("bailing out. If telepresence version isn't working, nothing will: %w", err))
		s.Require().NoError(err)
	}
	s.Empty(stderr)
	s.Contains(stdout, fmt.Sprintf("Client: %s", s.TelepresenceVersion()))
}

func (s *cliSuite) Test_Status() {
	stdout, stderr, err := itest.Telepresence(s.Context(), "status")
	if err != nil {
		s.SetGeneralError(fmt.Errorf("bailing out. If telepresence status isn't working, nothing will: %w", err))
		s.Require().NoError(err)
	}
	s.NoError(err)
	s.Empty(stderr)
	s.Contains(stdout, "Root Daemon: Not running")
	s.Contains(stdout, "User Daemon: Not running")
}
