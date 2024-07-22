package integration_test

import (
	"strings"
	"time"

	"github.com/telepresenceio/telepresence/v2/integration_test/itest"
)

type argoRolloutsSuite struct {
	itest.Suite
	itest.NamespacePair
}

func (s *argoRolloutsSuite) SuiteName() string {
	return "ArgoRollouts"
}

func init() {
	itest.AddTrafficManagerSuite("-argo-rollouts", func(h itest.NamespacePair) itest.TestingSuite {
		return &argoRolloutsSuite{Suite: itest.Suite{Harness: h}, NamespacePair: h}
	})
}

func (s *argoRolloutsSuite) SetupSuite() {
	s.Suite.SetupSuite()
	s.TelepresenceHelmInstallOK(s.Context(), true, "--set", "argoRollouts.enabled=true")
	s.TelepresenceConnect(s.Context())
}

func (s *argoRolloutsSuite) Test_SuccessfullyInterceptsArgoRollout() {
	ctx := s.Context()
	require := s.Require()

	tp, svc, port := "Rollout", "echo-argo-rollout", "9094"
	s.ApplyApp(ctx, svc, strings.ToLower(tp)+"/"+svc)
	defer s.DeleteSvcAndWorkload(ctx, "deploy", "echo-auto-inject")

	require.Eventually(
		func() bool {
			stdout, _, err := itest.Telepresence(ctx, "list")
			return err == nil && strings.Contains(stdout, svc)
		},
		6*time.Second, // waitFor
		2*time.Second, // polling interval
	)

	stdout := itest.TelepresenceOk(ctx, "intercept", "--mount", "false", "--port", port, svc)
	require.Contains(stdout, "Using "+tp+" "+svc)
	stdout = itest.TelepresenceOk(ctx, "list", "--intercepts")
	require.Contains(stdout, svc+": intercepted")
	require.NotContains(stdout, "Volume Mount Point")
	s.CapturePodLogs(ctx, svc, "traffic-agent", s.AppNamespace())
	itest.TelepresenceOk(ctx, "leave", svc)
	stdout = itest.TelepresenceOk(ctx, "list", "--intercepts")
	require.NotContains(stdout, svc+": intercepted")

	itest.TelepresenceDisconnectOk(ctx)

	dfltCtx := itest.WithUser(ctx, "default")
	itest.TelepresenceOk(dfltCtx, "connect", "--namespace", s.AppNamespace(), "--manager-namespace", s.ManagerNamespace())
	itest.TelepresenceOk(dfltCtx, "uninstall", "--agent", svc)
	itest.TelepresenceDisconnectOk(dfltCtx)
	s.TelepresenceConnect(ctx)

	require.Eventually(
		func() bool {
			stdout, _, err := itest.Telepresence(ctx, "list", "--agents")
			return err == nil && !strings.Contains(stdout, svc)
		},
		180*time.Second, // waitFor
		6*time.Second,   // polling interval
	)
}
