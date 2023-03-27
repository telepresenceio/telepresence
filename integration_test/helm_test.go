package integration_test

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/stretchr/testify/suite"

	"github.com/telepresenceio/telepresence/v2/integration_test/itest"
)

type helmSuite struct {
	itest.Suite
	itest.SingleService
	mgrSpace2 string
	appSpace2 string
}

func init() {
	itest.AddSingleServiceSuite("", "echo", func(h itest.SingleService) suite.TestingSuite {
		s := &helmSuite{Suite: itest.Suite{Harness: h}, SingleService: h}
		suffix := itest.GetGlobalHarness(h.HarnessContext()).Suffix()
		s.appSpace2, s.mgrSpace2 = itest.AppAndMgrNSName(suffix + "-2")
		return s
	})
}

func (s *helmSuite) SetupSuite() {
	s.Suite.SetupSuite()
	ctx := s.Context()
	itest.CreateNamespaces(ctx, s.appSpace2, s.mgrSpace2)
	itest.ApplyEchoService(ctx, s.ServiceName(), s.appSpace2, 80)
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
	_, stderr, err := itest.Telepresence(itest.WithUser(ctx, "default"), "intercept",
		"--namespace", s.appSpace2, "--mount", "false", s.ServiceName(), "--port", "9090")
	s.Error(err)
	s.True(
		strings.Contains(stderr, `No interceptable deployment, replicaset, or statefulset matching echo found`) ||
			strings.Contains(stderr, `cannot get resource "deployments" in API group "apps" in the namespace`))
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
	defer func() {
		ctx := s.Context()
		itest.TelepresenceDisconnectOk(ctx)
		itest.TelepresenceOk(ctx, "connect", "--manager-namespace", s.ManagerNamespace())
	}()

	s.Run("Installs Successfully", func() {
		ctx := itest.WithNamespaces(s.Context(), &itest.Namespaces{
			Namespace:         s.mgrSpace2,
			ManagedNamespaces: []string{s.appSpace2},
		})
		s.NoError(itest.Kubectl(ctx, s.mgrSpace2, "apply", "-f", filepath.Join("testdata", "k8s", "client_sa.yaml")))
		itest.TelepresenceDisconnectOk(ctx)
		s.NoError(s.TelepresenceHelmInstall(ctx, false))
	})

	s.Run("Can be connected to", func() {
		ctx := itest.WithUser(s.Context(), s.mgrSpace2+":"+itest.TestUser)
		stdout := itest.TelepresenceOk(ctx, "connect", "--manager-namespace", s.mgrSpace2)
		s.Contains(stdout, "Connected to context")
		s.Eventually(func() bool {
			return itest.Run(ctx, "curl", "--silent", "--connect-timeout", "1", fmt.Sprintf("%s.%s", svc, s.appSpace2)) == nil
		}, 7*time.Second, 1*time.Second)
	})

	s.Run("Can intercept", func() {
		ctx := s.Context()
		defer itest.TelepresenceOk(ctx, "leave", svc+"-"+s.appSpace2)
		stdout := itest.TelepresenceOk(ctx, "intercept", "--namespace", s.appSpace2, "--mount", "false", svc, "--port", "9090")
		s.Contains(stdout, "Using Deployment "+svc)
		stdout = itest.TelepresenceOk(ctx, "list", "--namespace", s.appSpace2, "--intercepts")
		s.Contains(stdout, svc+": intercepted")
	})

	s.Run("Uninstalls Successfully", func() {
		defer itest.TelepresenceQuitOk(s.Context())
		s.UninstallTrafficManager(s.Context(), s.mgrSpace2)
	})
}

func (s *helmSuite) Test_CollidingInstalls() {
	ctx := itest.WithNamespaces(s.Context(), &itest.Namespaces{
		Namespace:         s.AppNamespace(),
		ManagedNamespaces: []string{s.appSpace2},
	})
	s.Error(s.TelepresenceHelmInstall(ctx, false))
}
