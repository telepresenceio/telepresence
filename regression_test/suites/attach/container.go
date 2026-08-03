package attach

import (
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
// Test_ContainerReplace additionally combines --container with the
// deprecated "intercept --replace" flag (not the dedicated "replace"
// command, whose own --container instead names the container to replace
// directly). cmd/traffic/cmd/manager/state/intercept.go's
// getOrCreateAgentConfig resolves the container to mark
// agentconfig.ReplacePolicyContainer through that same
// Sidecar.FindIntercept call, so --replace combined with --container removes
// the *named* container from the pod
// (cmd/traffic/cmd/manager/mutator/agent_injector.go's
// maybeRemoveAppContainer) even though forwarded traffic keeps flowing
// through whichever container's port the Service actually exposes.
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

// Test_ContainerReplace proves --container also selects which container
// --replace acts on: with --container secondary, the secondary container is
// the one removed from the pod spec, not the primary one whose port is
// actually forwarded to the local echo. Detaching restores it, and a plain
// intercept on the same workload afterwards proves the pod is back to its
// original two-container shape with the primary as the default env source.
func (s *Container) Test_ContainerReplace() {
	t := s.T()
	conn := s.Connect()
	wl := s.Workload(containerWorkload("container-replace"))
	ls := s.LocalEcho()

	a := conn.Intercept(t, wl, rt.ToLocal(ls, "http"), cli.MountFalse(), cli.Container("secondary"), cli.Replace())
	waitRollout(&s.Suite, wl)
	rt.RoutedToLocal(t, wl.ServiceURL(), ls)
	s.Require().NotNil(a.Intercept, "intercept response should include InterceptInfo")
	s.Equal("secondary", a.Intercept.Environment["TP_TEST_TAG"])
	assertPodContainers(&s.Suite, wl, true, false)

	a.Detach(t)
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
