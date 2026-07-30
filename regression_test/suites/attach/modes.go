// Package attach holds suites that attach (intercept or ingest) to a
// workload and detach again.
package attach

import (
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/cli"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/managers"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/workloads"
)

// AttachModes proves attach/detach for {intercept, ingest} x {Deployment,
// StatefulSet}. Intercept additionally proves that the service is routed to
// the local marker while attached, and back to the cluster after detach.
type AttachModes struct {
	rt.Suite
}

func init() {
	rt.Register(&AttachModes{},
		rt.InArea("attach"),
		rt.NeedsManager(managers.Default),
		rt.WithLabels(rt.CompatCore),
	)
}

// attachKind pairs a subtest-name suffix with the workload template it uses.
type attachKind struct {
	name string
	tpl  workloads.Template
}

// Test_Attach runs the {intercept, ingest} x {deployment, statefulset}
// table, one subtest per cell, with deterministic names.
func (s *AttachModes) Test_Attach() {
	kinds := []attachKind{
		{"deployment", workloads.Echo("attach-deployment")},
		{"statefulset", workloads.EchoStatefulSet("attach-statefulset")},
	}
	for _, verb := range []string{"intercept", "ingest"} {
		for _, k := range kinds {
			s.Run(verb+"/"+k.name, func() { s.runAttach(verb, k.tpl) })
		}
	}
}

// runAttach attaches to tpl's workload, confirms `list` shows it, and (for
// intercept only) confirms the service routes to the local marker until
// detach routes it back to the cluster.
func (s *AttachModes) runAttach(verb string, tpl workloads.Template) {
	t := s.T()
	conn := s.Connect()
	wl := s.Workload(tpl)
	ls := s.LocalEcho()

	var a *rt.Attach
	switch verb {
	case "intercept":
		a = conn.Intercept(t, wl, rt.ToLocal(ls, "http"), cli.MountFalse())
	case "ingest":
		a = conn.Ingest(t, wl, cli.MountFalse())
	default:
		t.Fatalf("unknown verb %q", verb)
	}

	s.True(listContains(conn.List(t), wl.Name, wl.Namespace), "list should show %s.%s", wl.Name, wl.Namespace)

	url := wl.ServiceURL()
	if verb == "intercept" {
		rt.RoutedToLocal(t, url, ls)
	}

	a.Detach(t)

	if verb == "intercept" {
		rt.RoutedToCluster(t, url)
	}
}

// listContains reports whether entries contains a workload named name in ns.
func listContains(entries []cli.ListEntry, name, ns string) bool {
	for _, e := range entries {
		if e.Name == name && e.Namespace == ns {
			return true
		}
	}
	return false
}
