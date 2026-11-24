package session

import (
	"context"

	"github.com/telepresenceio/telepresence/v2/regression_test/framework/managers"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
)

// wshardValidSuffix is the valid DNS suffix that the traffic-manager serves in this suite.
const wshardValidSuffix = "wshard-valid.internal"

// wshardInvalidSuffixes holds one valid DNS suffix and several names that are
// not DNS names but are valid as parts of a file path.
//
//nolint:gochecknoglobals // constant list
var wshardInvalidSuffixes = []string{
	wshardValidSuffix,
	"../wshard-escape",
	"x/../../wshard-escape",
	"wshard/slash.internal",
	"wshard bad.internal",
}

// wshardSpec makes the traffic-manager serve the suffixes above.
//
//nolint:gochecknoglobals // catalog spec, referenced by both Register and the tests
var wshardSpec = managers.ClientConfig("wshard-dns", managers.Values{
	Client: managers.Client{
		DNS: managers.ClientDNS{IncludeSuffixes: wshardInvalidSuffixes},
	},
})

// WshardManagerConfig proves that a connecting client does not adopt DNS names
// that the traffic-manager serves and that are not valid DNS names.
type WshardManagerConfig struct {
	rt.Suite
}

func init() {
	rt.Register(&WshardManagerConfig{}, rt.InArea("session"), rt.NeedsManager(wshardSpec))
}

// wshardQuitOnCleanup stops every daemon when the test ends and makes the
// framework forget its memoized connections.
func wshardQuitOnCleanup(s *rt.Suite) {
	t := s.T()
	t.Cleanup(func() {
		r := rt.R()
		if _, _, err := r.CLI().Run(context.Background(), "quit", "-s"); err != nil {
			r.Infof("[rtest] WshardManagerConfig: quit -s: %v", err)
		}
		r.ForgetConnections()
	})
}

// Test_InvalidDNSNamesAreDropped serves DNS suffixes that are not DNS names
// and asserts that the root daemon's include suffixes keep the valid one and
// none of the others.
func (s *WshardManagerConfig) Test_InvalidDNSNamesAreDropped() {
	t := s.T()
	ctx := s.Ctx()
	ns := s.AppNamespace()

	freeDefaultConnection(t, ns)
	wshardQuitOnCleanup(&s.Suite)
	rt.Reconnect(t, ctx, ns)

	var st daemonStatus
	s.Require().NoError(s.CLI().JSON(ctx, &st, "status", "--format", "json"))
	s.Contains(st.RootDaemon.DNS.IncludeSuffixes, wshardValidSuffix, "the valid suffix must survive")
	for _, name := range wshardInvalidSuffixes[1:] {
		s.NotContains(st.RootDaemon.DNS.IncludeSuffixes, name, "invalid DNS name %q must be dropped", name)
	}
}
