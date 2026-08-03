package injector

import (
	"os"
	"path/filepath"

	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/telepresenceio/telepresence/v2/pkg/annotation"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/managers"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/workloads"
)

// limitRangeManifest is a min/max-only LimitRange with no explicit
// default/defaultRequest, so the LimitRanger admission controller derives
// both the default limit (from max) and the default request (from that
// default) at 100Mi.
const limitRangeManifest = `apiVersion: v1
kind: LimitRange
metadata:
  name: rtest-mem-limits
spec:
  limits:
    - type: Container
      min:
        memory: 20Mi
      max:
        memory: 100Mi
`

// limitRangeDefaultMemory is the quantity the LimitRange above defaults both
// requests and limits to, absent an explicit default.
const limitRangeDefaultMemory = "100Mi"

// LimitRange proves that a namespace's LimitRange defaults reach the
// injected traffic-agent container: an annotated workload with no
// resources of its own still ends up with the LimitRange's defaulted
// requests/limits.
type LimitRange struct {
	rt.Suite
}

func init() {
	rt.Register(&LimitRange{}, rt.InArea("injector"), rt.NeedsManager(managers.Default))
}

func (s *LimitRange) Test_AgentGetsDefaultedResources() {
	t := s.T()
	ctx := s.Ctx()
	s.Manager()
	env := rt.Env{Ctx: ctx, T: t, R: s.R()}
	ns := rt.PrivateNamespace(env, "limitrange")
	// ns is brand new: the already-running manager only manages it once its
	// pod restarts and re-lists namespaces.
	s.Require().NoError(rt.RestartManager(env))

	path := filepath.Join(s.R().ArtifactDir("manifests"), "limitrange-"+ns+".yaml")
	if err := os.WriteFile(path, []byte(limitRangeManifest), 0o644); err != nil {
		t.Fatalf("writing LimitRange manifest: %v", err)
	}
	if _, err := s.R().Kubectl(ctx, ns, "apply", "-f", path); err != nil {
		t.Fatalf("applying LimitRange: %v", err)
	}

	tpl := workloads.Echo("limitrange-echo")
	tpl.Annotations = map[string]string{annotation.InjectTrafficAgent: "enabled"}
	wl := rt.Get(t, rt.WorkloadFixture(ns, tpl))

	s.Eventually(func() bool {
		return hasAgentContainer(ctx, s.R(), wl.Namespace, wl.Name)
	}, agentPollTimeout, agentPollInterval, "traffic-agent should be injected")

	want, err := resource.ParseQuantity(limitRangeDefaultMemory)
	s.Require().NoError(err)

	rr, err := agentResources(ctx, s.R(), wl.Namespace, wl.Name)
	s.Require().NoError(err)
	reqMem := rr.Requests.Memory()
	limMem := rr.Limits.Memory()
	s.True(reqMem != nil && reqMem.Equal(want),
		"traffic-agent request memory should default to %s, got %v", limitRangeDefaultMemory, reqMem)
	s.True(limMem != nil && limMem.Equal(want),
		"traffic-agent limit memory should default to %s, got %v", limitRangeDefaultMemory, limMem)
}
