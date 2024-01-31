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

func (is *installSuite) limitedRangeTest() {
	const svc = "echo"
	ctx := itest.WithUser(is.Context(), is.ManagerNamespace()+":"+itest.TestUser)
	is.TelepresenceConnect(ctx)
	itest.TelepresenceOk(ctx, "loglevel", "debug")

	require := is.Require()
	itest.ApplyEchoService(ctx, svc, is.AppNamespace(), 8083)
	defer func() {
		is.NoError(itest.Kubectl(ctx, is.AppNamespace(), "delete", "svc,deploy", svc))
		is.Eventually(func() bool { return len(itest.RunningPods(ctx, svc, is.AppNamespace())) == 0 }, 2*time.Minute, 6*time.Second)
	}()

	_, _, err := itest.Telepresence(ctx, "intercept", "--mount", "false", svc)
	if err != nil {
		if out, err := itest.KubectlOut(ctx, is.AppNamespace(), "get", "pod", "-o", "yaml", "-l", "app="+svc); err == nil {
			dlog.Info(ctx, out)
		}
	}
	require.NoError(err)
	is.Eventually(
		func() bool {
			stdout, _, err := itest.Telepresence(ctx, "list", "--intercepts")
			return err == nil && strings.Contains(stdout, svc+": intercepted")
		},
		10*time.Second,
		2*time.Second,
	)
	itest.TelepresenceOk(ctx, "leave", svc)

	// Ensure that LimitRange is injected into traffic-agent
	out, err := itest.KubectlOut(ctx, is.AppNamespace(), "get", "pods", "-l", "app="+svc, "-o",
		`jsonpath={range .items.*.spec.containers[?(@.name=='traffic-agent')]}{.resources}{","}{end}`)
	require.NoError(err)
	dlog.Infof(ctx, "resources = %s", out)
	var rrs []v1.ResourceRequirements
	require.NoError(json.Unmarshal([]byte("["+strings.TrimSuffix(out, ",")+"]"), &rrs))
	oneGig, err := resource.ParseQuantity("100Mi")
	require.NoError(err)
	require.NotEmpty(rrs)
	rr := rrs[0]
	m := rr.Limits.Memory()
	require.True(m != nil && m.Equal(oneGig))
	m = rr.Requests.Memory()
	require.True(m != nil && m.Equal(oneGig))
}

func (is *installSuite) TestLimitRange() {
	ctx := is.Context()
	require := is.Require()
	require.NoError(itest.Kubectl(ctx, is.ManagerNamespace(), "apply", "-f", filepath.Join("testdata", "k8s", "client_sa.yaml")))
	defer func() {
		require.NoError(itest.Kubectl(ctx, is.ManagerNamespace(), "delete", "-f", filepath.Join("testdata", "k8s", "client_sa.yaml")))
	}()

	defer is.UninstallTrafficManager(ctx, is.ManagerNamespace())

	require.NoError(itest.Kubectl(ctx, is.AppNamespace(), "apply", "-f", filepath.Join("testdata", "k8s", "memory-constraints.yaml")))
	defer func() {
		require.NoError(itest.Kubectl(ctx, is.AppNamespace(), "delete", "-f", filepath.Join("testdata", "k8s", "memory-constraints.yaml")))
	}()

	is.Run("Never", func() {
		is.NoError(is.TelepresenceHelmInstall(is.Context(), false, "--set", "agentInjector.webhook.reinvocationPolicy=Never"))
		is.limitedRangeTest()
	})

	is.Run("IfNeeded", func() {
		is.NoError(is.TelepresenceHelmInstall(is.Context(), true, "--set", "agentInjector.webhook.reinvocationPolicy=IfNeeded"))
		is.limitedRangeTest()
	})
}
