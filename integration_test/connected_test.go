package integration_test

import (
	"fmt"

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

func (s *connectedSuite) Test_ReportsVersionFromDaemon() {
	stdout := itest.TelepresenceOk(s.Context(), "version")
	s.Contains(stdout, fmt.Sprintf("Client: %s", s.TelepresenceVersion()))
	s.Contains(stdout, fmt.Sprintf("Root Daemon: %s", s.TelepresenceVersion()))
	s.Contains(stdout, fmt.Sprintf("User Daemon: %s", s.TelepresenceVersion()))
}

func (s *connectedSuite) Test_Status() {
	stdout := itest.TelepresenceOk(s.Context(), "status")
	s.Contains(stdout, "Root Daemon: Running")
	s.Contains(stdout, "User Daemon: Running")
	s.Contains(stdout, "Kubernetes context:")
}
