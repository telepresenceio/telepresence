package install

import (
	"path/filepath"

	"github.com/telepresenceio/telepresence/v2/regression_test/framework/managers"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
)

// SetupRoutingProbe proves that `telepresence setup`'s routing-conflict
// probe (pkg/client/cli/setup/probe_routing.go) recognizes an
// already-connected session's own routes and excludes them from the
// reported conflicts. Unlike Setup, which probes fresh, disconnected
// namespaces, this suite needs a live connection to the shared manager.
type SetupRoutingProbe struct {
	rt.Suite
}

func init() {
	rt.Register(&SetupRoutingProbe{}, rt.InArea("install"), rt.NeedsManager(managers.Default))
}

// setupRoutingSummary is the subset of `setup --format json`'s structured
// report (render.go's Summary) this test reads.
type setupRoutingSummary struct {
	Facts struct {
		Routing struct {
			Summary struct {
				Verdict string `json:"verdict"`
			} `json:"summary"`
			ActiveSessionSubnets []string `json:"activeSessionSubnets"`
		} `json:"routing"`
	} `json:"facts"`
}

// Test_ConnectedSessionRoutesIgnored connects to the shared manager, then
// runs `setup --format json` against the same cluster and checks that the
// routing probe reports no conflicts and lists the connection's own routed
// subnets as excluded.
func (s *SetupRoutingProbe) Test_ConnectedSessionRoutesIgnored() {
	ctx := s.Ctx()
	r := s.R()
	ns := s.AppNamespace()

	s.Connect()

	var st rootDaemonSubnets
	s.Eventually(func() bool {
		if err := s.CLI().JSON(ctx, &st, "status", "--format", "json"); err != nil {
			return false
		}
		return len(st.RootDaemon.Subnets) > 0
	}, subnetPollTimeout, subnetPollInterval, "connect should map at least one subnet")
	s.Require().NotEmpty(st.RootDaemon.Subnets, "test needs at least one routed subnet to prove exclusion")

	dir := r.ArtifactDir("setup")
	output := filepath.Join(dir, "routing-probe-"+ns+".yaml")

	var summary setupRoutingSummary
	err := s.CLI().JSON(ctx, &summary, "setup",
		"--manager-namespace", managers.ManagerNamespace, "--format", "json",
		"--output", output, "--non-interactive")
	s.Require().NoError(err, "setup --format json")

	// Only the session routes that overlap a cluster subnet setup knows about
	// are excluded, so the excluded set is a non-empty subset of what the root
	// daemon routes.
	s.Equal("yes", summary.Facts.Routing.Summary.Verdict, "routing probe should report no conflicts")
	s.NotEmpty(summary.Facts.Routing.ActiveSessionSubnets, "the connection's routed subnets should be excluded")
	for _, sn := range summary.Facts.Routing.ActiveSessionSubnets {
		s.Contains(st.RootDaemon.Subnets, sn, "excluded subnet %s should be one the session routes", sn)
	}
}
