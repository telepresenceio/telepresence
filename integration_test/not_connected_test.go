package integration_test

import (
	"github.com/stretchr/testify/suite"

	"github.com/telepresenceio/telepresence/v2/integration_test/itest"
)

type notConnectedSuite struct {
	itest.Suite
	itest.NamespacePair
}

func init() {
	itest.AddNamespacePairSuite("", func(h itest.NamespacePair) suite.TestingSuite {
		return &notConnectedSuite{Suite: itest.Suite{Harness: h}, NamespacePair: h}
	})
}

func (s *notConnectedSuite) SetupSuite() {
	s.Suite.SetupSuite()
	ctx := itest.WithUser(s.Context(), "default")
	stdout := itest.TelepresenceOk(ctx, "connect")
	s.Contains(stdout, "Launching Telepresence Root Daemon")
	s.Contains(stdout, "Launching Telepresence User Daemon")
	s.Contains(stdout, "Connected to context")
	itest.TelepresenceQuitOk(ctx)
}

func (s *notConnectedSuite) Test_ConnectWithCommand() {
	ctx := s.Context()
	stdout := itest.TelepresenceOk(ctx, "connect", "--", s.Executable(), "status")
	s.Contains(stdout, "Launching Telepresence Root Daemon")
	s.Contains(stdout, "Launching Telepresence User Daemon")
	s.Contains(stdout, "Connected to context")
	s.Contains(stdout, "Kubernetes context:")
	s.Regexp(`Telepresence proxy:\s+ON`, stdout)
	if s.T().Failed() {
		s.CapturePodLogs(ctx, "app=traffic-manager", "", s.ManagerNamespace())
	}
	itest.TelepresenceQuitOk(ctx)
}

func (s *notConnectedSuite) Test_InvalidKubeconfig() {
	ctx := itest.WithEnv(s.Context(), map[string]string{"KUBECONFIG": "/dev/null"})
	stdout, stderr, err := itest.Telepresence(ctx, "connect")
	s.Contains(stderr, "kubeconfig has no context definition")
	s.Contains(stdout, "Launching Telepresence Root Daemon")
	s.Contains(stdout, "Launching Telepresence User Daemon")
	itest.AssertQuitOutput(ctx, stdout)
	s.Error(err)
}

func (s *notConnectedSuite) Test_NonExistentContext() {
	ctx := s.Context()
	stdout, stderr, err := itest.Telepresence(ctx, "connect", "--context", "not-likely-to-exist")
	s.Error(err)
	s.Contains(stderr, `"not-likely-to-exist" does not exist`)
	s.Contains(stdout, "Launching Telepresence Root Daemon")
	s.Contains(stdout, "Launching Telepresence User Daemon")
	itest.AssertQuitOutput(ctx, stdout)
}
