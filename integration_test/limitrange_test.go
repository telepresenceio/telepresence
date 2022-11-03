package integration_test

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/integration_test/itest"
)

func (s *helmSuite) limitedRangeTest(origCtx context.Context, policy, limitedNS string) {
	ctx := itest.WithEnv(origCtx, map[string]string{"TELEPRESENCE_MANAGER_NAMESPACE": limitedNS})
	svc := s.ServiceName()
	defer func() {
		s.UninstallTrafficManager(ctx, limitedNS)
		itest.TelepresenceOk(ctx, "quit", "-s")
	}()
	s.NoError(s.InstallTrafficManager(ctx, map[string]string{"agentInjector.webhook.reinvocationPolicy": policy}, limitedNS))
	itest.TelepresenceOk(ctx, "connect")
	itest.TelepresenceOk(ctx, "loglevel", "debug")
	s.CapturePodLogs(ctx, "app=traffic-manager", "", limitedNS)

	require := s.Require()
	require.NoError(itest.Kubectl(itest.WithModuleRoot(ctx), limitedNS, "apply", "-f", filepath.Join("k8s", "memory-constraints.yaml")))
	itest.ApplyEchoService(ctx, svc, limitedNS, 8083)
	defer func() {
		s.NoError(itest.Kubectl(ctx, limitedNS, "delete", "svc,deploy", svc))
		s.Eventually(func() bool { return len(itest.RunningPods(ctx, svc, limitedNS)) == 0 }, 2*time.Minute, 6*time.Second)
	}()

	_, _, err := itest.Telepresence(ctx, "intercept", "--namespace", limitedNS, "--mount", "false", svc)
	if err != nil {
		if out, err := itest.KubectlOut(ctx, limitedNS, "get", "pod", "-o", "yaml", "-l", "app="+svc); err == nil {
			dlog.Info(ctx, out)
		}
	}
	require.NoError(err)
	s.Eventually(
		func() bool {
			stdout := itest.TelepresenceOk(ctx, "list", "--namespace", limitedNS, "--intercepts")
			return strings.Contains(stdout, svc+": intercepted")
		},
		10*time.Second,
		2*time.Second,
	)
	itest.TelepresenceOk(ctx, "leave", svc+"-"+limitedNS)

	// Ensure that LimitRange is injected into traffic-agent
	out, err := itest.KubectlOut(ctx, limitedNS, "get", "pods", "-l", "app="+svc, "-o",
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

func (s *helmSuite) TestLimitRange() {
	ctx := s.Context()
	itest.TelepresenceOk(ctx, "quit", "-s")
	defer func() {
		itest.TelepresenceOk(ctx, "connect")
	}()

	limitedNS := fmt.Sprintf("limited-ns-%s", s.Suffix())
	itest.CreateNamespaces(ctx, limitedNS)
	defer itest.DeleteNamespaces(ctx, limitedNS)

	s.Run("Never", func() {
		s.limitedRangeTest(s.Context(), "Never", limitedNS)
	})

	s.Run("IfNeeded", func() {
		s.limitedRangeTest(s.Context(), "IfNeeded", limitedNS)
	})
}
