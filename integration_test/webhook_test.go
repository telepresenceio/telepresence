package integration_test

import (
	"fmt"
	"strings"
	"time"

	"github.com/telepresenceio/telepresence/v2/integration_test/itest"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
)

type webhookSuite struct {
	itest.Suite
	itest.TrafficManager
}

func (s *webhookSuite) SuiteName() string {
	return "Webhook"
}

func init() {
	itest.AddConnectedSuite("", func(h itest.TrafficManager) itest.TestingSuite {
		return &webhookSuite{Suite: itest.Suite{Harness: h}, TrafficManager: h}
	})
}

func (s *webhookSuite) Test_AutoInjectedAgent() {
	ctx := s.Context()
	s.ApplyApp(ctx, "echo-auto-inject", "deploy/echo-auto-inject")
	defer s.DeleteSvcAndWorkload(ctx, "deploy", "echo-auto-inject")

	require := s.Require()
	verb := "engage"
	if !s.ClientIsVersion(">2.21.x") {
		verb = "intercept"
	}
	require.Eventually(func() bool {
		stdout, _, err := itest.Telepresence(ctx, "list", "--agents")
		return err == nil && strings.Contains(stdout, fmt.Sprintf("echo-auto-inject: ready to %s (traffic-agent already installed)", verb))
	},
		20*time.Second, // waitFor
		2*time.Second,  // polling interval
		"doesn't show up with agent installed in list output",
	)

	stdout := itest.TelepresenceOk(ctx, "intercept", "--mount", "false", "echo-auto-inject", "--port", "9091")
	defer itest.TelepresenceOk(ctx, "leave", "echo-auto-inject")
	require.Contains(stdout, "Using Deployment echo-auto-inject")
	stdout = itest.TelepresenceOk(ctx, "list", "--intercepts")
	require.Contains(stdout, "echo-auto-inject: intercepted")
}

func (s *notConnectedSuite) Test_AgentImageFromConfig() {
	// Use a config with agentImage to validate that it's the
	// latter that is used in the traffic-manager
	ctx := itest.WithConfig(s.Context(), func(cfg client.Config) {
		cfg.Images().PrivateAgentImage = "imageFromConfig:0.0.1"
	})

	s.TelepresenceHelmInstallOK(itest.WithAgentImage(ctx, nil), true)
	defer s.RollbackTM(ctx)

	s.TelepresenceConnect(ctx)
	defer itest.TelepresenceQuitOk(ctx)

	st := itest.TelepresenceStatusOk(ctx)
	s.Require().NotNil(st.TrafficManager)
	s.Equal(s.ManagerRegistry()+"/imageFromConfig:0.0.1", st.TrafficManager.TrafficAgent)
}
