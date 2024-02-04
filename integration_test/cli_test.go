package integration_test

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"

	"github.com/telepresenceio/telepresence/v2/integration_test/itest"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
)

type cliSuite struct {
	itest.Suite
}

func (s *cliSuite) SuiteName() string {
	return "CLI"
}

func init() {
	itest.AddClusterSuite(func(ctx context.Context) itest.TestingSuite {
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
	s.Regexp(fmt.Sprintf(`Client\s*: %s`, regexp.QuoteMeta(s.TelepresenceVersion())), stdout)
}

func (s *cliSuite) Test_VersionWithInvalidKubeContext() {
	stdout, _, err := itest.Telepresence(itest.WithEnv(s.Context(), map[string]string{
		"KUBECONFIG": "file-that-does-not-exist",
	}), "version")
	if err != nil {
		s.Require().NoError(err)
	}

	s.Regexp(fmt.Sprintf(`Client\s*: %s`, regexp.QuoteMeta(s.TelepresenceVersion())), stdout)
}

func (s *cliSuite) Test_Help() {
	// TODO: Fix these tests
	s.T().Skip("these tests don't work")
	const (
		helpHead  = `Telepresence can connect to a cluster and route all outbound traffic`
		usageHead = `Usage:`
	)

	stdout, stderr, err := itest.Telepresence(s.Context(), "help")
	if err != nil {
		s.SetGeneralError(fmt.Errorf("bailing out. If telepresence help isn't working, nothing will: %w", err))
		s.Require().NoError(err)
	}
	s.Empty(stderr)
	s.Contains(stdout, helpHead)
	s.Contains(stdout, usageHead)

	stdout, stderr, err = itest.Telepresence(s.Context(), "--help")
	if err != nil {
		s.SetGeneralError(fmt.Errorf("bailing out. If telepresence --help isn't working, nothing will: %w", err))
		s.Require().NoError(err)
	}
	s.Empty(stderr)
	s.Contains(stdout, helpHead)
	s.Contains(stdout, usageHead)

	stdout, stderr, err = itest.Telepresence(s.Context(), "-h")
	if err != nil {
		s.SetGeneralError(fmt.Errorf("bailing out. If telepresence --help isn't working, nothing will: %w", err))
		s.Require().NoError(err)
	}
	s.Empty(stderr)
	s.Contains(stdout, helpHead)
	s.Contains(stdout, usageHead)

	stdout, stderr, err = itest.Telepresence(s.Context(), "")
	if err != nil {
		s.SetGeneralError(fmt.Errorf("bailing out. If telepresence --help isn't working, nothing will: %w", err))
		s.Require().NoError(err)
	}
	s.Empty(stderr)
	s.Contains(stdout, helpHead)
	s.Contains(stdout, usageHead)
}

func (s *cliSuite) Test_Status() {
	itest.TelepresenceQuitOk(s.Context())
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

func (s *cliSuite) Test_StatusWithJSONFlag() {
	itest.TelepresenceQuitOk(s.Context())
	stdout, stderr, err := itest.Telepresence(s.Context(), "status", "--json")
	if err != nil {
		s.SetGeneralError(fmt.Errorf("bailing out. If telepresence status isn't working, nothing will: %w", err))
		s.Require().NoError(err)
	}
	s.NoError(err)
	s.Empty(stderr)

	var status itest.StatusResponse
	s.NoError(json.Unmarshal([]byte(stdout), &status))
	s.False(status.RootDaemon.Running)
	s.False(status.UserDaemon.Running)
}

func (s *cliSuite) Test_StatusWithJSON() {
	itest.TelepresenceQuitOk(s.Context())
	status, err := itest.TelepresenceStatus(s.Context())
	if err != nil {
		s.SetGeneralError(fmt.Errorf("bailing out. If telepresence status isn't working, nothing will: %w", err))
		s.Require().NoError(err)
	}
	s.False(status.RootDaemon.Running)
	s.False(status.UserDaemon.Running)
}

func (s *cliSuite) Test_ConfigViewClientOnly() {
	ctx := itest.WithConfig(s.Context(), func(c client.Config) {
		c.Timeouts().PrivateConnectivityCheck = 0
	})
	out := itest.TelepresenceOk(ctx, "config", "view", "--client-only")
	// Ensure that zero (but not default) values are included in output
	s.Regexp(regexp.MustCompile(`\sconnectivityCheck\s*:\s*0s\n`), out)
}
