package routing

import (
	"net/netip"
	"slices"

	"github.com/telepresenceio/telepresence/v2/regression_test/framework/managers"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
)

// neverProxySubCIDRBits is the prefix length carved out of the cluster's
// first routed (service) subnet for --never-proxy: narrow enough to always
// be a strict sub-CIDR of a real cluster service subnet (typically /12 to
// /24), matching the spec's "first /28 inside it".
const neverProxySubCIDRBits = 28

// NeverProxyOmitted proves a --never-proxy sub-CIDR of an already-routed
// subnet is honored, not silently dropped as pointless, without disturbing
// the parent subnet's own routing.
//
// Mirrors proxy_via_test.go's Test_NeverProxySubnetIsOmitted (lines 251-326),
// adapted from a whole-subnet never-proxy to a strict sub-CIDR. The old test
// never-proxies an entire routed subnet verbatim: pkg/client/rootd/
// session.go's shouldProxySubnet (827-849) then excludes that subnet from
// `subnets` altogether (it's wholly *covered* by the never-proxy entry), so
// by the time computeNeverProxyOverrides (1165-1180) checks whether any
// remaining routed subnet overlaps it, none does, and the entry is dropped
// as redundant -- logged "Dropping never-proxy %q because it is not
// routed" (session.go:1173), which is what the old test greps daemon.log
// for. A strict sub-CIDR is different: shouldProxySubnet's cover check
// requires the never-proxy entry to cover the *candidate* subnet, and a
// narrower sub-CIDR can never cover its own (wider) parent, so the parent
// stays in `subnets` and stays fully routed; computeNeverProxyOverrides then
// finds the parent *overlaps* the sub-CIDR, so the sub-CIDR survives into
// status.root_daemon.never_proxy_subnets instead of being dropped. This
// framework exposes no daemon.log access (rt.Runtime's log directory is
// unexported), so the old test's log-line assertion is ported onto that
// status field instead: the meaningful proof that the sub-CIDR was honored,
// not silently discarded.
type NeverProxyOmitted struct {
	rt.Suite
}

func init() {
	rt.Register(&NeverProxyOmitted{}, rt.InArea("routing"), rt.NeedsManager(managers.Default))
}

// Test_SubCIDRExcludedFromRoutedSubnets connects once to derive a /28
// sub-CIDR of the cluster's first routed subnet, quits, then reconnects with
// --never-proxy <sub-CIDR> (--never-proxy confirmed as the exact flag name
// in pkg/client/cli/daemon/request.go:100-102's StringSliceVar) and checks
// that the sub-CIDR never appears as a routed subnet in its own right while
// the parent subnet stays fully routed and the sub-CIDR itself shows up in
// never_proxy_subnets.
func (s *NeverProxyOmitted) Test_SubCIDRExcludedFromRoutedSubnets() {
	t := s.T()
	ctx := s.Ctx()
	ns := s.AppNamespace()
	s.Manager()

	conn := rt.Mutate(t, rt.ConnectionFixture(ns))
	var before daemonStatus
	s.Eventually(func() bool {
		if err := s.CLI().JSON(ctx, &before, "status", "--format", "json"); err != nil {
			return false
		}
		return len(before.RootDaemon.Subnets) > 0
	}, subnetPollTimeout, subnetPollInterval, "connect should map at least one subnet")
	conn.Disconnect(t)
	if len(before.RootDaemon.Subnets) == 0 {
		t.Skip("test cannot run unless the client maps at least one subnet")
	}

	parent, err := netip.ParsePrefix(before.RootDaemon.Subnets[0])
	s.Require().NoError(err)
	if parent.Bits() >= neverProxySubCIDRBits {
		t.Skipf("routed subnet %s is already as narrow as, or narrower than, /%d", parent, neverProxySubCIDRBits)
	}
	subCIDR := netip.PrefixFrom(parent.Addr(), neverProxySubCIDRBits)

	conn = rt.Mutate(t, rt.ConnectionFixture(ns, rt.ConnExtraArgs("--never-proxy", subCIDR.String())))
	defer conn.Disconnect(t)

	var st daemonStatus
	s.Eventually(func() bool {
		if err := s.CLI().JSON(ctx, &st, "status", "--format", "json"); err != nil {
			return false
		}
		return slices.Contains(st.RootDaemon.NeverProxySubnets, subCIDR.String())
	}, subnetPollTimeout, subnetPollInterval,
		"never-proxied sub-CIDR %s should be honored (present in never_proxy_subnets), not dropped as unrouted",
		subCIDR)

	s.NotContains(st.RootDaemon.Subnets, subCIDR.String(),
		"never-proxied sub-CIDR %s should not appear as a routed subnet in its own right", subCIDR)
	s.Contains(st.RootDaemon.Subnets, parent.String(),
		"parent subnet %s should remain fully routed", parent)
}
