package integration_test

import (
	"strings"
	"time"

	"github.com/stretchr/testify/suite"

	"github.com/telepresenceio/telepresence/v2/integration_test/itest"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
)

type webhookSuite struct {
	itest.Suite
	itest.NamespacePair
}

func init() {
	itest.AddConnectedSuite("", func(h itest.NamespacePair) suite.TestingSuite {
		return &webhookSuite{Suite: itest.Suite{Harness: h}, NamespacePair: h}
	})
}

func (s *webhookSuite) Test_AutoInjectedAgent() {
	ctx := s.Context()
	s.ApplyApp(ctx, "echo-auto-inject", "deploy/echo-auto-inject")
	defer func() {
		s.NoError(s.Kubectl(ctx, "delete", "svc,deploy", "echo-auto-inject"))
	}()

	require := s.Require()
	require.Eventually(func() bool {
		stdout := itest.TelepresenceOk(ctx, "list", "--namespace", s.AppNamespace(), "--agents")
		return strings.Contains(stdout, "echo-auto-inject: ready to intercept (traffic-agent already installed)")
	},
		20*time.Second, // waitFor
		2*time.Second,  // polling interval
		"doesn't show up with agent installed in list output",
	)

	stdout := itest.TelepresenceOk(ctx, "intercept", "--namespace", s.AppNamespace(), "--mount", "false", "echo-auto-inject", "--port", "9091")
	defer itest.TelepresenceOk(ctx, "leave", "echo-auto-inject-"+s.AppNamespace())
	require.Contains(stdout, "Using Deployment echo-auto-inject")
	stdout = itest.TelepresenceOk(ctx, "list", "--namespace", s.AppNamespace(), "--intercepts")
	require.Contains(stdout, "echo-auto-inject: intercepted")
}

func (s *notConnectedSuite) Test_WebhookAgentImageFromConfig() {
	// Restore the traffic-manager at the end of this function
	ctx := itest.WithUser(s.Context(), "default")
	defer func() {
		itest.TelepresenceOk(ctx, "connect")
		itest.TelepresenceDisconnectOk(ctx)
	}()

	// Use a config with agentImage and webhookAgentImage to validate that it's the
	// latter that is used in the traffic-manager
	ctxAI := itest.WithConfig(ctx, &client.Config{
		Images: client.Images{
			PrivateAgentImage:        "notUsed:0.0.1",
			PrivateWebhookAgentImage: "imageFromConfig:0.0.1",
		},
	})

	// Remove the traffic-manager since we are altering config that applies to
	// creating the traffic-manager
	uninstallEverything := func() {
		stdout := itest.TelepresenceOk(ctx, "uninstall", "--everything")
		itest.AssertQuitOutput(ctx, stdout)
		s.Require().Eventually(
			func() bool {
				stdout, _ := itest.KubectlOut(ctx, s.ManagerNamespace(),
					"get", "svc,deploy", "traffic-manager", "--ignore-not-found")
				return stdout == ""
			},
			5*time.Second,        // waitFor
			500*time.Millisecond, // polling interval
		)
	}
	uninstallEverything()

	// And reinstall it
	itest.TelepresenceOk(ctxAI, "connect")

	// When this function ends we uninstall the manager
	defer func() {
		uninstallEverything()
	}()

	image, err := itest.Output(ctx, "kubectl",
		"--namespace", s.ManagerNamespace(),
		"get", "deploy", "traffic-manager",
		"--ignore-not-found",
		"-o",
		"jsonpath={.spec.template.spec.containers[0].env[?(@.name=='TELEPRESENCE_AGENT_IMAGE')].value}")

	require := s.Require()
	require.NoError(err)
	actualRegistry, err := itest.KubectlOut(ctx, s.ManagerNamespace(),
		"get", "deploy", "traffic-manager",
		"--ignore-not-found",
		"-o",
		"jsonpath={.spec.template.spec.containers[0].env[?(@.name=='TELEPRESENCE_REGISTRY')].value}")
	require.NoError(err)
	s.Equal("imageFromConfig:0.0.1", image)
	s.Equal(s.Registry(), actualRegistry)
	s.CapturePodLogs(ctx, "app=traffic-manager", "", s.ManagerNamespace())
}
