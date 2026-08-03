// Package smoke holds the basic CLI/connectivity proof suites: SmokeCLI runs
// with no traffic-manager, SmokeConnected against one.
package smoke

import (
	"strings"

	"github.com/telepresenceio/telepresence/v2/regression_test/framework/cli"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
)

// SmokeCLI checks CLI commands that need no traffic-manager and no
// connection: version, status, and config view.
type SmokeCLI struct {
	rt.Suite
}

func init() {
	rt.Register(&SmokeCLI{}, rt.InArea("smoke"))
}

// Test_Version checks that `version --format json` reports the client
// version matching the binary under test.
func (s *SmokeCLI) Test_Version() {
	var v cli.Version
	s.Require().NoError(s.CLI().JSON(s.Ctx(), &v, "version", "--format", "json"))
	s.Equal("v"+s.R().Version().String(), v.Client)
}

// Test_StatusNotRunning checks `status --format json` while no daemon is
// connected. A dev-mode run may have adopted a daemon left running by an
// earlier invocation, and this suite runs before any other suite connects,
// so `quit -s` first guarantees the clean slate the assertion needs.
func (s *SmokeCLI) Test_StatusNotRunning() {
	t := s.T()
	if _, stderr, err := s.CLI().Run(s.Ctx(), "quit", "-s"); err != nil {
		t.Fatalf("quit -s: %v\n%s", err, stderr)
	}
	s.R().ForgetConnections()
	var st cli.Status
	s.Require().NoError(s.CLI().JSON(s.Ctx(), &st, "status", "--format", "json"))
	s.False(st.UserDaemon.Running)
	s.False(st.RootDaemon.Running)
	s.Empty(st.TrafficManager.Name)
}

// Test_ConfigView checks that `config view --client-only` succeeds and
// prints the local client configuration.
func (s *SmokeCLI) Test_ConfigView() {
	t := s.T()
	out := s.CLI().OK(t, "config", "view", "--client-only")
	s.NotEmpty(strings.TrimSpace(out))
}
