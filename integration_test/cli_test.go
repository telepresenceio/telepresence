package integration_test

import (
	"context"
	"encoding/json"
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

func (s *cliSuite) Test_StatusWithJSON() {
	itest.TelepresenceQuitOk(s.Context())
	stdout, stderr, err := itest.Telepresence(s.Context(), "status", "--json")
	if err != nil {
		s.SetGeneralError(fmt.Errorf("bailing out. If telepresence status isn't working, nothing will: %w", err))
		s.Require().NoError(err)
	}
	s.NoError(err)
	s.Empty(stderr)

	var status statusResponse
	s.NoError(json.Unmarshal([]byte(stdout), &status))
	s.False(status.RootDaemon.Running)
	s.False(status.UserDaemon.Running)
}

type statusResponseRootDaemon struct {
	Running           bool     `json:"running,omitempty"`
	AlsoProxySubnets  []string `json:"also_proxy_subnets,omitempty"`
	NeverProxySubnets []string `json:"never_proxy_subnets,omitempty"`
}

type statusResponseUserDaemon struct {
	Running           bool   `json:"running,omitempty"`
	KubernetesContext string `json:"kubernetes_context,omitempty"`
	InstallID         string `json:"install_id,omitempty"`
}

type statusResponse struct {
	RootDaemon statusResponseRootDaemon `json:"root_daemon,omitempty"`
	UserDaemon statusResponseUserDaemon `json:"user_daemon,omitempty"`
}
