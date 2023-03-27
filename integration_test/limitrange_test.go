package integration_test

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"time"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/integration_test/itest"
)

func (s *installSuite) limitedRangeTest() {
	const svc = "echo"
	ctx := itest.WithUser(s.Context(), s.ManagerNamespace()+":"+itest.TestUser)
	itest.TelepresenceOk(ctx, "connect", "--manager-namespace", s.ManagerNamespace())
	itest.TelepresenceOk(ctx, "loglevel", "debug")

	require := s.Require()
	itest.ApplyEchoService(ctx, svc, s.AppNamespace(), 8083)
	defer func() {
		s.NoError(itest.Kubectl(ctx, s.AppNamespace(), "delete", "svc,deploy", svc))
		s.Eventually(func() bool { return len(itest.RunningPods(ctx, svc, s.AppNamespace())) == 0 }, 2*time.Minute, 6*time.Second)
	}()

	_, _, err := itest.Telepresence(ctx, "intercept", "--namespace", s.AppNamespace(), "--mount", "false", svc)
	if err != nil {
		if out, err := itest.KubectlOut(ctx, s.AppNamespace(), "get", "pod", "-o", "yaml", "-l", "app="+svc); err == nil {
			dlog.Info(ctx, out)
		}
	}
	require.NoError(err)
	s.Eventually(
		func() bool {
			stdout := itest.TelepresenceOk(ctx, "list", "--namespace", s.AppNamespace(), "--intercepts")
			return strings.Contains(stdout, svc+": intercepted")
		},
		10*time.Second,
		2*time.Second,
	)
	itest.TelepresenceOk(ctx, "leave", svc+"-"+s.AppNamespace())

	// Ensure that LimitRange is injected into traffic-agent
	out, err := itest.KubectlOut(ctx, s.AppNamespace(), "get", "pods", "-l", "app="+svc, "-o",
		"jsonpath={.items.*.spec.containers[?(@.name=='traffic-agent')].resources}")
	require.NoError(err)
	dlog.Infof(ctx, "resources = %s", out)
	var rrs []v1.ResourceRequirements
	require.NoError(json.Unmarshal([]byte("["+out+"]"), &rrs))
	oneGig, err := resource.ParseQuantity("100Mi")
	require.NoError(err)
	require.Len(rrs, 1)
	rr := rrs[0]
	m := rr.Limits.Memory()
	require.True(m != nil && m.Equal(oneGig))
	m = rr.Requests.Memory()
	require.True(m != nil && m.Equal(oneGig))
}

func (s *installSuite) TestLimitRange() {
	ctx := s.Context()
	require := s.Require()
	require.NoError(itest.Kubectl(ctx, s.ManagerNamespace(), "apply", "-f", filepath.Join("testdata", "k8s", "client_sa.yaml")))
	defer func() {
		require.NoError(itest.Kubectl(ctx, s.ManagerNamespace(), "delete", "-f", filepath.Join("testdata", "k8s", "client_sa.yaml")))
	}()

	defer s.UninstallTrafficManager(ctx, s.ManagerNamespace())

	require.NoError(itest.Kubectl(ctx, s.AppNamespace(), "apply", "-f", filepath.Join("testdata", "k8s", "memory-constraints.yaml")))
	defer func() {
		require.NoError(itest.Kubectl(ctx, s.AppNamespace(), "delete", "-f", filepath.Join("testdata", "k8s", "memory-constraints.yaml")))
	}()

	s.Run("Never", func() {
		s.NoError(s.TelepresenceHelmInstall(s.Context(), false, "--set", "agentInjector.webhook.reinvocationPolicy=Never"))
		s.limitedRangeTest()
	})

	s.Run("IfNeeded", func() {
		s.NoError(s.TelepresenceHelmInstall(s.Context(), true, "--set", "agentInjector.webhook.reinvocationPolicy=IfNeeded"))
		s.limitedRangeTest()
	})
}
