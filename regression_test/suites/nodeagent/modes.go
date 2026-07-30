package nodeagent

import (
	"time"

	"github.com/telepresenceio/telepresence/v2/regression_test/framework/check"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/cli"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/managers"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/workloads"
)

// httpFilterHeaderKey/httpFilterHeaderVal is the filter
// Test_HTTPFilteredIntercept routes on.
const (
	httpFilterHeaderKey = "x-rtest-nodeagent-filter"
	httpFilterHeaderVal = "match"
)

// wiretapObserveTimeout bounds how long Test_Wiretap waits for its local
// service to observe a copied request: a wiretap copy is async and
// best-effort, so the local side effect can lag the response.
const wiretapObserveTimeout = 10 * time.Second

// NodeAgentModes proves the four attach verbs over a node-agent Job instead
// of an injected sidecar: the target pod is left untouched, a Job appears
// while the attach is live, and it is reaped on detach.
type NodeAgentModes struct {
	rt.Suite
}

func init() {
	rt.Register(&NodeAgentModes{}, rt.InArea("nodeagent"), rt.NeedsManager(managers.NodeAgent()))
}

// Test_Intercept attaches a global node-agent intercept: traffic routes
// local, the pod is untouched, a Job appears, and detach reaps it.
func (s *NodeAgentModes) Test_Intercept() {
	t := s.T()
	ctx := s.Ctx()
	conn := s.Connect()
	wl := freshWorkload(t, ctx, s.R(), s.AppNamespace(), workloads.Echo("na-modes-intercept"))
	ls := s.LocalEcho()

	a := conn.Intercept(t, wl, rt.ToLocal(ls, "http"), cli.MountFalse(), nodeAgentFlag())
	mustDetach := true
	defer func() {
		if mustDetach {
			a.Detach(t)
		}
	}()

	rt.RoutedToLocal(t, wl.ServiceURL(), ls)
	waitJobCount(&s.Suite, ctx, wl, 1)
	s.False(hasAgentContainer(ctx, s.R(), wl.Namespace, wl.Name),
		"a node-agent intercept must not inject a traffic-agent sidecar")

	a.Detach(t)
	mustDetach = false
	waitJobCount(&s.Suite, ctx, wl, 0)
}

// Test_HTTPFilteredIntercept attaches an --http-header filtered node-agent
// intercept: header-matching traffic routes local, everything else keeps
// reaching the cluster through the node-agent's pass-through dial, the pod
// stays untouched, and detach reaps the Job.
func (s *NodeAgentModes) Test_HTTPFilteredIntercept() {
	t := s.T()
	ctx := s.Ctx()
	conn := s.Connect()
	wl := freshWorkload(t, ctx, s.R(), s.AppNamespace(), workloads.Echo("na-modes-http-intercept"))
	ls := s.LocalEcho()

	a := conn.Intercept(t, wl, rt.ToLocal(ls, "http"), cli.MountFalse(), nodeAgentFlag(),
		cli.HTTPHeader(httpFilterHeaderKey, httpFilterHeaderVal))
	mustDetach := true
	defer func() {
		if mustDetach {
			a.Detach(t)
		}
	}()

	url := wl.ServiceURL()
	rt.RoutedToLocal(t, url, ls, check.WithHeader(httpFilterHeaderKey, httpFilterHeaderVal))
	rt.RoutedToCluster(t, url)
	waitJobCount(&s.Suite, ctx, wl, 1)
	s.False(hasAgentContainer(ctx, s.R(), wl.Namespace, wl.Name),
		"a node-agent intercept must not inject a traffic-agent sidecar")

	a.Detach(t)
	mustDetach = false
	waitJobCount(&s.Suite, ctx, wl, 0)
}

// Test_Ingest attaches a node-agent ingest: the environment is sourced from
// the target, the pod stays untouched, a Job appears, and detach reaps it.
func (s *NodeAgentModes) Test_Ingest() {
	t := s.T()
	ctx := s.Ctx()
	conn := s.Connect()
	wl := freshWorkload(t, ctx, s.R(), s.AppNamespace(), workloads.Echo("na-modes-ingest"))

	a := conn.Ingest(t, wl, cli.MountFalse(), nodeAgentFlag())
	mustDetach := true
	defer func() {
		if mustDetach {
			a.Detach(t)
		}
	}()

	s.Require().NotNil(a.Ingest, "ingest response should include IngestInfo")
	s.Equal(wl.Name, a.Ingest.Environment["TELEPRESENCE_CONTAINER"])
	waitJobCount(&s.Suite, ctx, wl, 1)
	s.False(hasAgentContainer(ctx, s.R(), wl.Namespace, wl.Name),
		"a node-agent ingest must not inject a traffic-agent sidecar")

	a.Detach(t)
	mustDetach = false
	waitJobCount(&s.Suite, ctx, wl, 0)
}

// Test_Wiretap attaches a node-agent wiretap: the cluster keeps serving the
// original traffic while the local tap observes a copy, the pod stays
// untouched, and detach reaps the Job.
func (s *NodeAgentModes) Test_Wiretap() {
	t := s.T()
	ctx := s.Ctx()
	conn := s.Connect()
	wl := freshWorkload(t, ctx, s.R(), s.AppNamespace(), workloads.Echo("na-modes-wiretap"))
	ls := s.LocalEcho()

	a := conn.Wiretap(t, wl, rt.ToLocal(ls, "http"), cli.MountFalse(), nodeAgentFlag())
	mustDetach := true
	defer func() {
		if mustDetach {
			a.Detach(t)
		}
	}()
	s.True(a.Wiretap != nil && a.Wiretap.Wiretap, "wiretap response should report wiretap=true")

	url := wl.ServiceURL()
	rt.RoutedToCluster(t, url)
	s.Eventually(func() bool {
		return len(ls.Requests()) > 0
	}, wiretapObserveTimeout, 250*time.Millisecond,
		"local wiretap copy of %s.%s should have observed at least one request", wl.Name, wl.Namespace)

	waitJobCount(&s.Suite, ctx, wl, 1)
	s.False(hasAgentContainer(ctx, s.R(), wl.Namespace, wl.Name),
		"a node-agent wiretap must not inject a traffic-agent sidecar")

	a.Detach(t)
	mustDetach = false
	waitJobCount(&s.Suite, ctx, wl, 0)
}
