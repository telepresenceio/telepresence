package smoke

import (
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/cli"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/managers"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
)

// SmokeConnected checks CLI commands against a live connection: status,
// version, and list. Carries CompatCore: Test_Status and Test_Version
// exercise the manager's session lifecycle (ArriveAsClient, Remain, Depart,
// GetClientConfig, Version, GetAgentImageFQN) that every other compat-core
// test's Connect implicitly relies on; see framework/compat/manifest.go.
type SmokeConnected struct {
	rt.Suite
}

func init() {
	rt.Register(&SmokeConnected{},
		rt.InArea("smoke"),
		rt.NeedsManager(managers.Default),
		rt.WithLabels(rt.CompatCore),
	)
}

// Test_Status checks that `status --format json` reports both daemons and
// the traffic-manager as populated once connected.
func (s *SmokeConnected) Test_Status() {
	conn := s.Connect()
	st := conn.Status(s.T())
	s.True(st.UserDaemon.Running)
	s.True(st.RootDaemon.Running)
	s.NotEmpty(st.TrafficManager.Name)
}

// Test_Version checks that `version --format json` reports the client, both
// daemons, and the traffic-manager once connected.
func (s *SmokeConnected) Test_Version() {
	s.Connect()
	var v cli.Version
	s.Require().NoError(s.CLI().JSON(s.Ctx(), &v, "version", "--format", "json"))
	s.Equal("v"+s.R().Version().String(), v.Client)
	s.NotEmpty(v.UserDaemon)
	s.NotEmpty(v.RootDaemon)
	s.NotEmpty(v.TrafficManager)
}

// Test_ListExcludesManager checks that `list --format json` never reports
// the traffic-manager itself as a workload.
func (s *SmokeConnected) Test_ListExcludesManager() {
	conn := s.Connect()
	for _, e := range conn.List(s.T()) {
		s.NotEqual("traffic-manager", e.Name)
	}
}
