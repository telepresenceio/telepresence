package dns

import (
	"fmt"

	"github.com/telepresenceio/telepresence/v2/regression_test/framework/managers"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
)

// Wpad proves wpad.* name lookups fail while connected, instead of being
// forwarded to the cluster resolver. Mirrors the name forms
// integration_test/wpad_test.go's Test_WpadNotForwarded exercises for its
// "not forwarded" cases (its "wpad.bogus.nu" case is commented out there
// too), asserting the externally observable effect -- the lookup never
// resolves -- rather than that test's daemon.log NXDOMAIN/no-forward line
// inspection.
type Wpad struct {
	rt.Suite
}

func init() {
	rt.Register(&Wpad{}, rt.InArea("dns"), rt.NeedsManager(managers.Default))
}

// Test_NotForwarded connects and checks that every wpad.* name form fails to
// resolve (NXDOMAIN), never reaching the cluster's own DNS.
func (s *Wpad) Test_NotForwarded() {
	ctx := s.Ctx()
	ns := s.AppNamespace()
	s.Connect()

	names := []string{
		"wpad",
		fmt.Sprintf("wpad.%s", ns),
		"wpad.cluster.local",
		"wpad.svc.cluster.local",
		fmt.Sprintf("wpad.%s.svc.cluster.local", ns),
	}
	for _, name := range names {
		s.Run(name, func() {
			s.Eventually(func() bool {
				return !lookupSucceeds(ctx, name)
			}, lookupPollTimeout, lookupPollInterval, "%s unexpectedly resolved", name)
		})
	}
}
