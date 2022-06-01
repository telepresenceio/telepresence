package integration_test

import (
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

func (s *singleServiceSuite) TestLimitRange() {
	require := s.Require()
	ctx := s.Context()
	limitedNS := fmt.Sprintf("limited-ns-%s", s.Suffix())
	itest.CreateNamespaces(ctx, limitedNS)
	defer itest.DeleteNamespaces(ctx, limitedNS)
	require.NoError(itest.Kubectl(itest.WithModuleRoot(ctx), limitedNS, "apply", "-f", filepath.Join("k8s", "memory-constraints.yaml")))
	itest.ApplyEchoService(ctx, s.ServiceName(), limitedNS, 8083)

	itest.TelepresenceOk(ctx, "intercept", "--namespace", limitedNS, "--mount", "false", s.ServiceName())
	require.Eventually(
		func() bool {
			stdout := itest.TelepresenceOk(ctx, "list", "--namespace", limitedNS, "--intercepts")
			return strings.Contains(stdout, s.ServiceName()+": intercepted")
		},
		10*time.Second,
		2*time.Second,
	)
	itest.TelepresenceOk(ctx, "leave", s.ServiceName()+"-"+limitedNS)

	// Ensure that LimitRange is injected into traffic-agent
	out, err := itest.KubectlOut(ctx, limitedNS, "get", "pods", "-l", "app="+s.ServiceName(), "-o",
		"jsonpath={.items.*.spec.containers[?(@.name=='traffic-agent')].resources}")
	require.NoError(err)
	dlog.Infof(ctx, "resources = %s", out)
	var rrs []v1.ResourceRequirements
	require.NoError(json.Unmarshal([]byte("["+out+"]"), &rrs))
	oneGig, err := resource.ParseQuantity("1Gi")
	require.NoError(err)
	require.Len(rrs, 1)
	rr := rrs[0]
	m := rr.Limits.Memory()
	require.True(m != nil && m.Equal(oneGig))
	m = rr.Requests.Memory()
	require.True(m != nil && m.Equal(oneGig))
}
