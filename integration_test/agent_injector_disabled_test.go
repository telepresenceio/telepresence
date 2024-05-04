package integration_test

import (
	"bufio"
	"os"

	"github.com/telepresenceio/telepresence/v2/integration_test/itest"
)

type interceptDisabledSuite struct {
	itest.Suite
	itest.NamespacePair
}

func (s *interceptDisabledSuite) SuiteName() string {
	return "InterceptDisabled"
}

func init() {
	itest.AddNamespacePairSuite("", func(h itest.NamespacePair) itest.TestingSuite {
		return &interceptDisabledSuite{Suite: itest.Suite{Harness: h}, NamespacePair: h}
	})
}

func (s *interceptDisabledSuite) Test_AgentInjectorDisabled() {
	ctx := s.Context()
	tmLogName := func() string {
		tmLogName := s.TelepresenceHelmInstallOK(ctx, false, "--set", "agentInjector.enabled=false")
		defer s.UninstallTrafficManager(ctx, s.ManagerNamespace())

		s.TelepresenceConnect(ctx)
		defer itest.TelepresenceQuitOk(ctx)
		return tmLogName
	}()

	tmLogFile, err := os.Open(tmLogName)
	s.Require().NoError(err)
	defer tmLogFile.Close()

	sc := bufio.NewScanner(tmLogFile)
	t := s.T()
	for sc.Scan() {
		t.Log(sc.Text())
	}
}
