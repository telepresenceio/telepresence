// Package attach holds suites that attach (intercept, ingest, replace, or
// wiretap) to a workload and detach again.
package attach

import (
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/managers"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/workloads"
)

// AttachModes proves the compat-core smoke path: intercept a Deployment,
// confirm list shows it and the service routes to the local marker while
// attached, then confirm detach routes it back to the cluster. The full
// {intercept, ingest, replace, wiretap} x {deployment, statefulset,
// headless, no-service, multiport} table lives in AttachMatrix, which does
// not carry the CompatCore label: only this single cell does.
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

// Test_Attach runs the suite's one compat-core cell: intercept/deployment.
func (s *AttachModes) Test_Attach() {
	s.Run("intercept/deployment", func() {
		runCell(&s.Suite, "intercept", workloads.Echo("attach-deployment"))
	})
}
