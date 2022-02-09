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
	s.Contains(stdout, "Connected to context")
	s.CapturePodLogs(ctx, "app=traffic-manager", "", s.ManagerNamespace())
	itest.TelepresenceDisconnectOk(ctx)
}

func (s *notConnectedSuite) Test_ConnectWithCommand() {
	ctx := s.Context()
	stdout := itest.TelepresenceOk(ctx, "connect", "--", s.Executable(), "status")
	s.Contains(stdout, "Connected to context")
	s.Contains(stdout, "Kubernetes context:")
	itest.TelepresenceDisconnectOk(ctx)
}

func (s *notConnectedSuite) Test_InvalidKubeconfig() {
	ctx := s.Context()
	itest.TelepresenceOk(ctx, "quit", "-ur")
	badEnvCtx := itest.WithEnv(ctx, map[string]string{"KUBECONFIG": "/dev/null"})
	_, stderr, err := itest.Telepresence(badEnvCtx, "connect")
	s.Contains(stderr, "kubeconfig has no context definition")
	itest.TelepresenceQuitOk(ctx) // process is started with bad env, so get rid of it
	s.Error(err)
}

func (s *notConnectedSuite) Test_NonExistentContext() {
	ctx := s.Context()

	_, stderr, err := itest.Telepresence(ctx, "connect", "--context", "not-likely-to-exist")
	s.Error(err)
	s.Contains(stderr, `"not-likely-to-exist" does not exist`)
	itest.TelepresenceDisconnectOk(ctx)
}
