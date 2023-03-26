package integration_test

import (
	"encoding/json"
	"fmt"
	"regexp"

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
	s.NotContains(stdout, "traffic-manager")
}

func (s *connectedSuite) Test_ReportsAllVersions() {
	stdout := itest.TelepresenceOk(s.Context(), "version")
	rxVer := regexp.QuoteMeta(s.TelepresenceVersion())
	s.Regexp(fmt.Sprintf(`Client\s*: %s`, rxVer), stdout)
	s.Regexp(fmt.Sprintf(`Root Daemon\s*: %s`, rxVer), stdout)
	s.Regexp(fmt.Sprintf(`User Daemon\s*: %s`, rxVer), stdout)
	s.Regexp(fmt.Sprintf(`Traffic Manager\s*: %s`, rxVer), stdout)
}

func (s *connectedSuite) Test_Status() {
	stdout := itest.TelepresenceOk(s.Context(), "status")
	s.Contains(stdout, "Root Daemon: Running")
	s.Contains(stdout, "User Daemon: Running")
	s.Contains(stdout, "Kubernetes context:")
	s.Regexp(`Manager namespace\s+: `+s.ManagerNamespace(), stdout)
}

func (s *connectedSuite) Test_StatusWithJSON() {
	stdout := itest.TelepresenceOk(s.Context(), "status", "--output", "json")
	var status statusResponse
	s.NoError(json.Unmarshal([]byte(stdout), &status))
	s.True(status.RootDaemon.Running)
	s.True(status.UserDaemon.Running)
	s.NotEmpty(status.UserDaemon.KubernetesContext)
	s.NotEmpty(status.UserDaemon.InstallID)
	s.Equal(status.UserDaemon.ManagerNamespace, s.ManagerNamespace())
}
