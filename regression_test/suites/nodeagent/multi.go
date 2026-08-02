package nodeagent

import (
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/check"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/cli"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/managers"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/workloads"
)

// multiWorkloadName/multiReplicas is the 4-replica workload every test in
// this suite shares: WorkloadFixture memoizes by (namespace, template), so
// both test methods reuse the same rendered Deployment.
const (
	multiWorkloadName = "na-multi"
	multiReplicas     = 4
)

// multiRequestCount is how many sequential requests each routing assertion
// below sends: enough to sample more than one replica's node-agent Job.
const multiRequestCount = 20

// multiHeaderKey/multiHeaderVal is the filter Test_FilteredIntercept routes
// on.
const (
	multiHeaderKey = "x-rtest-nodeagent-multi"
	multiHeaderVal = "match"
)

// NodeAgentMulti proves node-agent mode across every replica of a
// multi-replica workload: a global intercept attaches to all of them, and
// an --http-header filtered intercept routes matching traffic to every
// replica while unmatched traffic keeps reaching the cluster.
type NodeAgentMulti struct {
	rt.Suite
}

func init() {
	rt.Register(&NodeAgentMulti{}, rt.InArea("nodeagent"), rt.NeedsManager(managers.NodeAgent()))
}

// Test_GlobalIntercept attaches an unfiltered node-agent intercept and
// proves multiRequestCount sequential requests are all served locally,
// regardless of which replica the service load-balanced them to.
func (s *NodeAgentMulti) Test_GlobalIntercept() {
	t := s.T()
	ctx := s.Ctx()
	conn := s.Connect()
	wl := s.Workload(workloads.EchoReplicas(multiWorkloadName, multiReplicas))
	ls := s.LocalEcho()

	a := conn.Intercept(t, wl, rt.ToLocal(ls, "http"), cli.MountFalse(), nodeAgentFlag())
	mustDetach := true
	defer func() {
		if mustDetach {
			a.Detach(t)
		}
	}()

	waitJobCount(&s.Suite, ctx, wl, multiReplicas)

	url := wl.ServiceURL()
	for range multiRequestCount {
		rt.RoutedToLocal(t, url, ls)
	}

	a.Detach(t)
	mustDetach = false
	waitJobCount(&s.Suite, ctx, wl, 0)
}

// Test_FilteredIntercept attaches an --http-header filtered node-agent
// intercept and proves header-matching requests are all served locally
// across every replica, while headerless requests keep reaching the
// cluster.
func (s *NodeAgentMulti) Test_FilteredIntercept() {
	t := s.T()
	ctx := s.Ctx()
	conn := s.Connect()
	wl := s.Workload(workloads.EchoReplicas(multiWorkloadName, multiReplicas))
	ls := s.LocalEcho()

	a := conn.Intercept(t, wl, rt.ToLocal(ls, "http"), cli.MountFalse(), nodeAgentFlag(),
		cli.HTTPHeader(multiHeaderKey, multiHeaderVal))
	mustDetach := true
	defer func() {
		if mustDetach {
			a.Detach(t)
		}
	}()

	waitJobCount(&s.Suite, ctx, wl, multiReplicas)

	url := wl.ServiceURL()
	for range multiRequestCount {
		rt.RoutedToLocal(t, url, ls, check.WithHeader(multiHeaderKey, multiHeaderVal))
	}
	for range 5 {
		rt.RoutedToCluster(t, url)
	}

	a.Detach(t)
	mustDetach = false
	waitJobCount(&s.Suite, ctx, wl, 0)
}
