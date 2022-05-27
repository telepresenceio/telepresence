package integration_test

import (
	"fmt"
	"strings"
	"time"

	"github.com/stretchr/testify/suite"

	"github.com/telepresenceio/telepresence/v2/integration_test/itest"
)

type helmSuite struct {
	itest.Suite
	itest.HelmAndService
	mgrSpace2 string
	appSpace2 string
}

func init() {
	itest.AddHelmAndServiceSuite("-1", "echo", func(h itest.HelmAndService) suite.TestingSuite {
		s := &helmSuite{Suite: itest.Suite{Harness: h}, HelmAndService: h}
		suffix := itest.GetGlobalHarness(h.HarnessContext()).Suffix()
		s.appSpace2, s.mgrSpace2 = itest.AppAndMgrNSName(suffix + "-2")
		return s
	})
}

func (s *helmSuite) SetupSuite() {
	s.Suite.SetupSuite()
	ctx := s.Context()
	itest.TelepresenceQuitOk(ctx)
	itest.CreateNamespaces(ctx, s.appSpace2, s.mgrSpace2)
	itest.ApplyEchoService(ctx, s.ServiceName(), s.appSpace2, 80)
	itest.TelepresenceOk(ctx, "connect")
}

func (s *helmSuite) TearDownSuite() {
	itest.DeleteNamespaces(s.Context(), s.appSpace2, s.mgrSpace2)
}

func (s *helmSuite) Test_HelmCanInterceptInManagedNamespace() {
	ctx := s.Context()
	defer itest.TelepresenceOk(ctx, "leave", s.ServiceName()+"-"+s.AppNamespace())

	stdout := itest.TelepresenceOk(ctx, "intercept", "--namespace", s.AppNamespace(), "--mount", "false", s.ServiceName(), "--port", "9090")
	s.Contains(stdout, "Using Deployment "+s.ServiceName())
	stdout = itest.TelepresenceOk(ctx, "list", "--namespace", s.AppNamespace(), "--intercepts")
	s.Contains(stdout, s.ServiceName()+": intercepted")
}

func (s *helmSuite) Test_HelmCannotInterceptInUnmanagedNamespace() {
	ctx := s.Context()
	_, stderr, err := itest.Telepresence(ctx, "intercept", "--namespace", s.appSpace2, "--mount", "false", s.ServiceName(), "--port", "9090")
	s.Error(err)
	s.Contains(stderr, `No interceptable deployment, replicaset, or statefulset matching echo found`)
}

func (s *helmSuite) Test_HelmWebhookInjectsInManagedNamespace() {
	ctx := s.Context()
	s.ApplyApp(ctx, "echo-auto-inject", "deploy/echo-auto-inject")
	defer s.DeleteSvcAndWorkload(ctx, "deploy", "echo-auto-inject")

	s.Eventually(func() bool {
		stdout, _, err := itest.Telepresence(ctx, "list", "--namespace", s.AppNamespace(), "--agents")
		return err == nil && strings.Contains(stdout, "echo-auto-inject: ready to intercept (traffic-agent already installed)")
	},
		20*time.Second, // waitFor
		2*time.Second,  // polling interval
	)
}

func (s *helmSuite) Test_HelmWebhookDoesntInjectInUnmanagedNamespace() {
	ctx := s.Context()
	itest.ApplyApp(ctx, "echo-auto-inject", s.appSpace2, "deploy/echo-auto-inject")
	defer itest.DeleteSvcAndWorkload(ctx, "deploy", "echo-auto-inject", s.appSpace2)

	s.Never(func() bool {
		stdout, _, err := itest.Telepresence(ctx, "list", "--namespace", s.appSpace2, "--agents")
		return err == nil && strings.Contains(stdout, "echo-auto-inject: ready to intercept (traffic-agent already installed)")
	},
		10*time.Second, // waitFor
		2*time.Second,  // polling interval
	)
}

func (s *helmSuite) Test_HelmMultipleInstalls() {
	svc := s.ServiceName()

	s.Run("Installs Successfully", func() {
		ctx := itest.WithEnv(s.Context(), map[string]string{"TELEPRESENCE_MANAGER_NAMESPACE": s.mgrSpace2})
		s.NoError(s.InstallTrafficManager(ctx, s.mgrSpace2, s.appSpace2))
	})

	s.Run("Can be connected to", func() {
		ctx := itest.WithEnv(s.Context(), map[string]string{"TELEPRESENCE_MANAGER_NAMESPACE": s.mgrSpace2})
		itest.TelepresenceQuitOk(ctx)
		stdout := itest.TelepresenceOk(ctx, "connect")
		s.Contains(stdout, "Connected to context")
		s.Eventually(func() bool {
			return itest.Run(ctx, "curl", "--silent", "--connect-timeout", "1", fmt.Sprintf("%s.%s", svc, s.appSpace2)) == nil
		}, 7*time.Second, 1*time.Second)
	})

	s.Run("Can intercept", func() {
		ctx := itest.WithEnv(s.Context(), map[string]string{"TELEPRESENCE_MANAGER_NAMESPACE": s.mgrSpace2})
		defer itest.TelepresenceQuitOk(ctx)
		stdout := itest.TelepresenceOk(ctx, "intercept", "--namespace", s.appSpace2, "--mount", "false", svc, "--port", "9090")
		s.Contains(stdout, "Using Deployment "+svc)
		stdout = itest.TelepresenceOk(ctx, "list", "--namespace", s.appSpace2, "--intercepts")
		s.Contains(stdout, svc+": intercepted")
	})

	s.Run("Uninstalls Successfully", func() {
		ctx := itest.WithEnv(s.Context(), map[string]string{"TELEPRESENCE_MANAGER_NAMESPACE": s.mgrSpace2})
		s.UninstallTrafficManager(ctx, s.mgrSpace2)
	})
}

func (s *helmSuite) Test_CollidingInstalls() {
	s.Error(s.InstallTrafficManager(s.Context(), s.mgrSpace2, s.AppNamespace(), s.appSpace2))
}
