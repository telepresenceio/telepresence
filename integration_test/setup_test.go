package integration_test

import (
	"context"
	"encoding/json/v2"
	"os"
	"path/filepath"

	"sigs.k8s.io/yaml"

	"github.com/telepresenceio/telepresence/v2/integration_test/itest"
	"github.com/telepresenceio/telepresence/v2/pkg/agentconfig"
	"github.com/telepresenceio/telepresence/v2/pkg/version"
)

// setupSuite exercises "telepresence setup" against a cluster without a
// pre-installed traffic-manager: setup installs (and upgrades) it itself, so
// the suite builds on the namespace-pair harness rather than the
// traffic-manager harness.
type setupSuite struct {
	itest.Suite
	itest.NamespacePair
}

func (s *setupSuite) SuiteName() string {
	return "Setup"
}

func init() {
	itest.AddNamespacePairSuite("-setup", func(h itest.NamespacePair) itest.TestingSuite {
		return &setupSuite{Suite: itest.Suite{Harness: h}, NamespacePair: h}
	})
}

func (s *setupSuite) SetupSuite() {
	if !(s.ClientVersion().EQ(version.Structured) && s.ManagerVersion().EQ(version.Structured)) {
		s.T().Skip("Not part of compatibility tests. telepresence setup requires the current client and manager.")
	}
	s.Suite.SetupSuite()
}

// TearDownSuite is the safety net: no test may leave a traffic-manager
// release behind, whatever passed or failed.
func (s *setupSuite) TearDownSuite() {
	ctx := s.Context()
	if s.trafficManagerInstalled(ctx) {
		s.UninstallTrafficManager(ctx, s.ManagerNamespace())
	}
	itest.TelepresenceQuit(ctx)
}

// setupArgs prepends the setup verb and the suite's manager namespace.
func (s *setupSuite) setupArgs(args ...string) []string {
	return append([]string{"setup", "--manager-namespace", s.ManagerNamespace()}, args...)
}

// trafficManagerInstalled reports whether a traffic-manager Helm release
// exists in the suite's manager namespace.
func (s *setupSuite) trafficManagerInstalled(ctx context.Context) bool {
	out, err := itest.Output(ctx, "helm", "list", "-n", s.ManagerNamespace(), "--output", "json")
	if err != nil {
		return false
	}
	var releases []map[string]any
	if err := json.Unmarshal([]byte(out), &releases); err != nil {
		return false
	}
	for _, r := range releases {
		if r["name"] == agentconfig.ManagerAppName {
			return true
		}
	}
	return false
}

// helmValues returns the operator-supplied values of the installed release.
func (s *setupSuite) helmValues(ctx context.Context) map[string]any {
	out, err := itest.Output(ctx, "helm", "get", "values", agentconfig.ManagerAppName, "-n", s.ManagerNamespace(), "--output", "json")
	s.Require().NoError(err)
	var values map[string]any
	s.Require().NoError(json.Unmarshal([]byte(out), &values))
	return values
}

func (s *setupSuite) Test_SetupApplyIdempotence() {
	ctx := s.Context()
	rq := s.Require()
	defer func() {
		if s.trafficManagerInstalled(ctx) {
			s.UninstallTrafficManager(ctx, s.ManagerNamespace())
		}
	}()

	stdout := itest.TelepresenceOk(ctx, s.setupArgs("--apply", "--non-interactive")...)
	s.Contains(stdout, "Action: install")
	s.Contains(stdout, "Applying...")
	s.Contains(stdout, "Traffic Manager installed successfully")
	s.Contains(stdout, "Verification:")
	s.True(s.trafficManagerInstalled(ctx), "expected a traffic-manager release after --apply")

	// A second apply concludes that there is nothing to change.
	stdout = itest.TelepresenceOk(ctx, s.setupArgs("--apply", "--non-interactive")...)
	s.Contains(stdout, "Action: none")
	s.Contains(stdout, "up to date")
	s.NotContains(stdout, "Applying...")

	// Formatted validation over the up-to-date installation.
	jsonOut := itest.TelepresenceOk(ctx, s.setupArgs("--non-interactive", "--format", "json")...)
	var sum map[string]any
	rq.NoError(json.Unmarshal([]byte(jsonOut), &sum))
	s.Equal("none", sum["action"])
}

func (s *setupSuite) Test_SetupRoundTrip() {
	ctx := s.Context()
	rq := s.Require()
	dir := itest.TempDir(ctx)
	f1 := filepath.Join(dir, "values-1.yaml")
	f2 := filepath.Join(dir, "values-2.yaml")

	itest.TelepresenceOk(ctx, s.setupArgs("--non-interactive", "--output", f1)...)
	stdout := itest.TelepresenceOk(ctx, s.setupArgs("--non-interactive", "--input", f1, "--output", f2)...)
	s.NotContains(stdout, "warning: the input pins")

	b1, err := os.ReadFile(f1)
	rq.NoError(err)
	b2, err := os.ReadFile(f2)
	rq.NoError(err)
	s.Equal(string(b1), string(b2), "an --output/--input round trip must be lossless")

	// A key the engine has no opinion about passes through to the result.
	f3 := filepath.Join(dir, "values-custom.yaml")
	f4 := filepath.Join(dir, "values-custom-out.yaml")
	rq.NoError(os.WriteFile(f3, append(b1, []byte("logLevel: debug\n")...), 0o644))
	stdout = itest.TelepresenceOk(ctx, s.setupArgs("--non-interactive", "--input", f3, "--output", f4)...)
	s.NotContains(stdout, "warning: the input pins")

	b4, err := os.ReadFile(f4)
	rq.NoError(err)
	var values map[string]any
	rq.NoError(yaml.Unmarshal(b4, &values))
	s.Equal("debug", values["logLevel"], "passthrough keys must survive the round trip")

	s.False(s.trafficManagerInstalled(ctx), "round trips must not mutate the cluster")
}

func (s *setupSuite) Test_SetupStreamOutput() {
	ctx := s.Context()
	rq := s.Require()

	stdout, stderr, err := itest.Telepresence(ctx, s.setupArgs("--non-interactive", "--output", "-")...)
	rq.NoError(err)

	var values map[string]any
	rq.NoError(yaml.Unmarshal([]byte(stdout), &values), "stdout must be nothing but the values YAML")
	s.Contains(values, "quicTunnel")
	s.Contains(values, "nodeAgent")
	s.NotContains(stdout, "Findings:")
	s.NotContains(stdout, "Action:")
	s.NotContains(stderr, "Findings:")
	s.NotContains(stderr, "Action:")
}

func (s *setupSuite) Test_SetupUpgradeMerge() {
	ctx := s.Context()
	rq := s.Require()
	s.TelepresenceHelmInstallOK(ctx, false)
	defer s.UninstallTrafficManager(ctx, s.ManagerNamespace())

	stdout := itest.TelepresenceOk(ctx, s.setupArgs("--apply", "--non-interactive", "--attach")...)
	s.Contains(stdout, "release: traffic-manager")
	s.Contains(stdout, "Changed from current installation:")
	s.Contains(stdout, "  - agentInjector.enabled")
	s.Contains(stdout, "  - nodeAgent.enabled")
	s.Contains(stdout, "  - quicTunnel.enabled")
	s.Contains(stdout, "Action: upgrade")
	s.Contains(stdout, "Traffic Manager upgraded successfully")

	// The merge must keep the values that the original install set.
	values := s.helmValues(ctx)
	s.Equal("debug", values["logLevel"], "pre-existing release values must survive the upgrade")
	nodeAgent, ok := values["nodeAgent"].(map[string]any)
	rq.True(ok, "expected a nodeAgent value block after the upgrade")
	s.Equal(true, nodeAgent["enabled"])
}

func (s *setupSuite) Test_SetupValidation() {
	ctx := s.Context()

	stdout := itest.TelepresenceOk(ctx, s.setupArgs("--non-interactive")...)
	s.Contains(stdout, "Findings:")
	s.Contains(stdout, "release: not installed")
	// The manager namespace exists (the harness created it) and kind's PSS is
	// permissive, so the admission canary confirms node-agent viability.
	s.Contains(stdout, "node-agent: yes")
	s.Contains(stdout, "Proposed configuration:")
	// QUIC is enabled either via an observed LoadBalancer capability or via
	// NodePort on a bare kind cluster; the service type may differ, the
	// conclusion may not.
	s.Contains(stdout, "  quicTunnel:\n    enabled: true")
	s.Contains(stdout, "Action: would-install")

	s.False(s.trafficManagerInstalled(ctx), "validation must not mutate the cluster")
}
