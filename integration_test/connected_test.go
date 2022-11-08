package integration_test

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/stretchr/testify/suite"

	"github.com/telepresenceio/telepresence/v2/integration_test/itest"
)

type connectedSuite struct {
	itest.Suite
	itest.NamespacePair
}

func init() {
	itest.AddConnectedSuite("", func(h itest.NamespacePair) suite.TestingSuite {
		return &connectedSuite{Suite: itest.Suite{Harness: h}, NamespacePair: h}
	})
}

func (s *connectedSuite) Test_ListExcludesTM() {
	stdout := itest.TelepresenceOk(s.Context(), "list", "-n", s.ManagerNamespace())
	// Drop the ambassador-agent which will include the word "traffic-manager"
	stdout = strings.Replace(stdout, "traffic-manager-ambassador-agent", "", 1)
	s.NotContains(stdout, "traffic-manager")
}

func (s *connectedSuite) Test_ReportsAllVersions() {
	stdout := itest.TelepresenceOk(s.Context(), "version")
	s.Contains(stdout, fmt.Sprintf("Client: %s", s.TelepresenceVersion()))
	s.Contains(stdout, fmt.Sprintf("Root Daemon: %s", s.TelepresenceVersion()))
	s.Contains(stdout, fmt.Sprintf("User Daemon: %s", s.TelepresenceVersion()))
	s.Contains(stdout, fmt.Sprintf("Traffic Manager: %s", s.TelepresenceVersion()))
}

func (s *connectedSuite) Test_ReportsNotConnected() {
	ctx := s.Context()
	itest.TelepresenceDisconnectOk(ctx)
	defer itest.TelepresenceOk(itest.WithUser(ctx, "default"), "connect")
	stdout := itest.TelepresenceOk(ctx, "version")
	s.Contains(stdout, fmt.Sprintf("Client: %s", s.TelepresenceVersion()))
	s.Contains(stdout, fmt.Sprintf("Root Daemon: %s", s.TelepresenceVersion()))
	s.Contains(stdout, fmt.Sprintf("User Daemon: %s", s.TelepresenceVersion()))
	s.Contains(stdout, "Traffic Manager: not connected")
}

func (s *connectedSuite) Test_Status() {
	stdout := itest.TelepresenceOk(s.Context(), "status")
	s.Contains(stdout, "Root Daemon: Running")
	s.Contains(stdout, "User Daemon: Running")
	s.Contains(stdout, "Kubernetes context:")
}

func (s *connectedSuite) Test_StatusWithJSON() {
	stdout := itest.TelepresenceOk(s.Context(), "status", "--output", "json")
	var status statusResponse
	s.NoError(json.Unmarshal([]byte(stdout), &status))
	s.True(status.RootDaemon.Running)
	s.True(status.UserDaemon.Running)
	s.NotEmpty(status.UserDaemon.KubernetesContext)
	s.NotEmpty(status.UserDaemon.InstallID)
}
