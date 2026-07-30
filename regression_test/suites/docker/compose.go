package docker

import (
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
)

// Compose is a placeholder for the docker-compose area, deferred past this
// wave; it registers and immediately skips so the gap stays visible in run
// reports instead of silently disappearing. See docs/plans/
// regression-test-framework/suite-catalog.md.
type Compose struct {
	rt.Suite
}

func init() {
	rt.Register(&Compose{},
		rt.InArea("docker"),
		rt.Requires(rt.Docker),
		rt.On("linux"),
	)
}

// Test_Deferred marks the compose area as intentionally unimplemented.
func (s *Compose) Test_Deferred() {
	s.T().Skip("compose area deferred; see suite-catalog.md")
}
