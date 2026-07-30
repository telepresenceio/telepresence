package attach

import (
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/managers"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/workloads"
)

// attachKind pairs a subtest-name suffix with the workload template it uses.
type attachKind struct {
	name string
	tpl  workloads.Template
}

// AttachMatrix extends AttachModes' single compat-core cell into the full
// {intercept, ingest, replace, wiretap} x {deployment, statefulset,
// headless, no-service, multiport} table. Every one of these cells is
// CLI-supported (no cell is skipped; see the package's PR description for
// the per-cell evidence read from pkg/client/cli and the manager). It
// carries no CompatCore label: only the intercept/deployment cell in
// AttachModes does.
type AttachMatrix struct {
	rt.Suite
}

func init() {
	rt.Register(&AttachMatrix{}, rt.InArea("attach"), rt.NeedsManager(managers.Default))
}

// Test_Attach runs every {verb, kind} cell except intercept/deployment,
// which AttachModes' compat-core cell already covers.
func (s *AttachMatrix) Test_Attach() {
	kinds := []attachKind{
		{"deployment", workloads.Echo("attach-matrix-deployment")},
		{"statefulset", workloads.EchoStatefulSet("attach-matrix-statefulset")},
		{"headless", workloads.EchoHeadless("attach-matrix-headless")},
		{"no-service", workloads.EchoNoService("attach-matrix-no-service")},
		{"multiport", workloads.EchoMultiPort("attach-matrix-multiport")},
	}
	verbs := []string{"intercept", "ingest", "replace", "wiretap"}
	for _, k := range kinds {
		for _, verb := range verbs {
			if verb == "intercept" && k.name == "deployment" {
				continue // covered by AttachModes' CompatCore cell
			}
			s.Run(verb+"/"+k.name, func() { runCell(&s.Suite, verb, k.tpl) })
		}
	}
}
