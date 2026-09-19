package attach

import (
	"net"
	"strconv"
	"time"

	"github.com/telepresenceio/telepresence/v2/regression_test/framework/cli"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/managers"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/workloads"
)

// secondaryContainerPort is the ExtraContainer's own container port,
// distinct from workloads.Echo's Port (8080): both containers share one
// pod/IP, so their ports must differ.
const secondaryContainerPort = 8081

// podConvergeTimeout/podConvergePollInterval bound assertPodContainers'
// wait for a rollout's outgoing pod to finish terminating, leaving exactly
// one pod to judge.
const (
	podConvergeTimeout      = 60 * time.Second
	podConvergePollInterval = 2 * time.Second
)

// containerWorkload returns a Deployment with a primary (app) container
// declaring TP_TEST_TAG=primary and a second container, "secondary",
// declaring TP_TEST_TAG=secondary. The Service targets only the primary's
// port, so every cell below probes the same ServiceURL regardless of which
// container --container names.
func containerWorkload(name string) workloads.Template {
	tpl := workloads.Echo(name)
	tpl.Env = map[string]string{"TP_TEST_TAG": "primary"}
	tpl.ExtraContainers = []workloads.ExtraContainer{{
		Name: "secondary",
		Port: secondaryContainerPort,
		Env:  map[string]string{"TP_TEST_TAG": "secondary"},
	}}
	// No Service covers the secondary's port; the annotation makes it
	// interceptable (pkg/annotation/annotation.go), so "replace" can
	// target the secondary container.
	tpl.Annotations = map[string]string{
		"telepresence.io/inject-container-ports": strconv.Itoa(secondaryContainerPort),
	}
	return tpl
}

// Container proves intercept's --container flag: which container's
// environment (and, by the same selection, its volumes -- mount content
// itself is the mounts area's concern, so every test here runs with
// --mount=false) an attach borrows.
//
// --container never changes which port is intercepted. On the intercept/
// wiretap command, its help text says exactly this (pkg/client/cli/
// intercept/command.go's AddInterceptFlags): "Name of container that
// provides the environment and mounts for the intercept. Defaults to the
// container matching the first intercepted port." The manager resolves both
// the env/mount source and the actually-routed port through the same call,
// pkg/agentconfig.Sidecar.FindIntercept (reached from
// cmd/traffic/cmd/manager/state/intercept.go's checkInterceptConsistency):
// it matches the service/port across every container's declared Intercepts
// first, then -- only when --container was set -- swaps the returned
// Container for the one named, leaving the matched port/Intercept, and so
// the traffic route, untouched. The agent applies that selection in
// cmd/traffic/cmd/agent/fwdstate.go's processRegularIntercept: it reports
// fs.containerStates[spec.ContainerName]'s own Env()/Mounts()
// (cmd/traffic/cmd/agent/containerstate.go) in the ReviewInterceptRequest,
// regardless of which container's port is actually being forwarded.
//
// Test_ContainerReplace additionally proves --container selects the
// container the "replace" command removes from the pod: pkg/icept/find.go's
// FindContainer resolves the container by name directly, and refuses a
// multi-container pod when no name is given.
type Container struct {
	rt.Suite
}

func init() {
	rt.Register(&Container{},
		rt.InArea("attach"),
		rt.NeedsManager(managers.Default),
	)
}

// Test_ContainerTargetsNamedContainer proves that --container secondary
// borrows the secondary container's environment for an intercept, while
// traffic still round-trips through the primary's Service port; a second,
// --container-less intercept on the same workload then proves the default
// falls back to the primary container.
func (s *Container) Test_ContainerTargetsNamedContainer() {
	t := s.T()
	conn := s.Connect()
	wl := s.Workload(containerWorkload("container-target"))
	ls := s.LocalEcho()

	a := conn.Intercept(t, wl, rt.ToLocal(ls, "http"), cli.MountFalse(), cli.Container("secondary"))
	rt.RoutedToLocal(t, wl.ServiceURL(), ls)
	s.Require().NotNil(a.Intercept, "intercept response should include InterceptInfo")
	s.Equal("secondary", a.Intercept.Environment["TP_TEST_TAG"])
	a.Detach(t)

	a = conn.Intercept(t, wl, rt.ToLocal(ls, "http"), cli.MountFalse())
	rt.RoutedToLocal(t, wl.ServiceURL(), ls)
	s.Require().NotNil(a.Intercept, "intercept response should include InterceptInfo")
	s.Equal("primary", a.Intercept.Environment["TP_TEST_TAG"])
	a.Detach(t)
}

// Test_ContainerReplace proves --container selects which container the
// "replace" command acts on: with --container secondary, the secondary
// container -- not the service-backed primary -- is removed from the pod
// and its port forwarded to the local echo, while the primary keeps
// serving the Service. Without --container, a two-container workload is
// refused. Detaching restores the pod, and a plain intercept afterwards
// proves the primary is the default env source again.
func (s *Container) Test_ContainerReplace() {
	t := s.T()
	conn := s.Connect()
	wl := s.Workload(containerWorkload("container-replace"))
	ls := s.LocalEcho()

	// A replace must name its container when the pod has more than one.
	stdout, stderr, err := s.CLI().Run(s.Ctx(), "replace", wl.Name, "-n", wl.Namespace, "--mount", "false")
	s.Error(err, "replace without --container should be refused on a two-container workload")
	s.Contains(stdout+stderr, "more than one container")

	// The secondary's port is service-less and unnamed, so it is targeted
	// by number.
	a := conn.Replace(t, wl, rt.ToLocal(ls, strconv.Itoa(secondaryContainerPort)), cli.MountFalse(), cli.Container("secondary"))
	waitRollout(&s.Suite, wl)
	s.Require().NotNil(a.Replace, "replace response should include InterceptInfo")
	s.Equal("secondary", a.Replace.Environment["TP_TEST_TAG"])
	assertPodContainers(&s.Suite, wl, true, false)

	// The Service still routes to the live primary, while the replaced
	// secondary's port routes to the local echo.
	rt.RoutedToCluster(t, wl.ServiceURL())
	rt.RoutedToLocal(t, "http://"+net.JoinHostPort(a.Replace.PodIP, strconv.Itoa(secondaryContainerPort)), ls)

	// replace names a --container attachment "<workload>/<container>"
	// (pkg/client/cli/intercept/command.go's ValidateReplace), so detach
	// must name the container explicitly too.
	_, stderr, err = s.CLI().Run(s.Ctx(), "detach", wl.Name, "--container", "secondary", "-n", wl.Namespace)
	s.Require().NoError(err, "detach: %s", stderr)
	waitRollout(&s.Suite, wl)
	assertPodContainers(&s.Suite, wl, true, true)

	a = conn.Intercept(t, wl, rt.ToLocal(ls, "http"), cli.MountFalse())
	rt.RoutedToLocal(t, wl.ServiceURL(), ls)
	s.Require().NotNil(a.Intercept, "intercept response should include InterceptInfo")
	s.Equal("primary", a.Intercept.Environment["TP_TEST_TAG"])
	a.Detach(t)
}

// assertPodContainers asserts wl's pods converge on a single pod (kubectl
// get pod -l app=<wl.Name>) that has, or lacks, containers named wl.Name
// (the primary) and "secondary". A rollout's outgoing pod can linger in
// Terminating after `rollout status` returns, so this polls until exactly
// one pod remains and its containers match, rather than judging the first
// listing.
func assertPodContainers(s *rt.Suite, wl *rt.Workload, wantPrimary, wantSecondary bool) {
	t := s.T()
	var lastSeen []map[string]bool
	s.Require().Eventually(func() bool {
		var pods struct {
			Items []struct {
				Spec struct {
					Containers []struct {
						Name string `json:"name"`
					} `json:"containers"`
				} `json:"spec"`
			} `json:"items"`
		}
		if err := s.R().KubectlJSON(s.Ctx(), wl.Namespace, &pods, "get", "pod", "-l", "app="+wl.Name); err != nil {
			t.Fatalf("get pod -l app=%s: %v", wl.Name, err)
		}
		lastSeen = lastSeen[:0]
		for _, p := range pods.Items {
			names := make(map[string]bool, len(p.Spec.Containers))
			for _, c := range p.Spec.Containers {
				names[c.Name] = true
			}
			lastSeen = append(lastSeen, names)
		}
		if len(lastSeen) != 1 {
			return false
		}
		return lastSeen[0][wl.Name] == wantPrimary && lastSeen[0]["secondary"] == wantSecondary
	}, podConvergeTimeout, podConvergePollInterval,
		"pods for %s never converged to one pod with primary=%v secondary=%v (last: %v)",
		wl.Name, wantPrimary, wantSecondary, &lastSeen)
}
