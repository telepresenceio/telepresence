package routing

import (
	"slices"
	"time"

	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/managers"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
)

// Conflicts proves the two ways telepresence handles a cluster subnet that
// collides with a local, already-routed one: auto-resolution replaces the
// conflicting subnet with the virtual subnet (mirrors cidr_conflict_test.go's
// Test_AutoConflictResolution, lines 94-123: `sns := st.RootDaemon.Subnets`,
// then `rq.NotContains(sns, c, ...)` + `rq.Contains(sns,
// virtualSubnetFor(c.Addr()), ...)`), and --allow-conflicting-subnets keeps
// the raw subnet routed as-is (mirrors its Test_AutoConflictAvoidance, lines
// 153-160: `s.Require().Equal(slice.AsStrings(s.subnets),
// slice.AsStrings(sns), ...)`). The conflict is manufactured by bringing up
// a local veth/bridge pair that owns an address inside the cluster's first
// service subnet, exactly as integration_test/testdata/scripts/veth-up.sh
// does for cidrConflictSuite -- reimplemented in veth.go with randomized
// interface names so two runs (or a leftover from a prior failed one) never
// collide.
type Conflicts struct {
	rt.Suite
}

func init() {
	rt.Register(&Conflicts{}, rt.InArea("routing"), rt.NeedsManager(managers.Default),
		rt.Requires(rt.Sudo), rt.On("linux"), rt.WithLabels(rt.Slow))
}

// Test_SubnetConflict connects once to read the currently routed subnets,
// quits, brings up a veth pair colliding with the first one, then runs the
// auto-resolve and --allow-conflicting-subnets cases as subtests sharing
// that one conflict. The veth pair always comes down via a defer (logged,
// not failed, on error -- a partially-failed vethUp can leave partial
// state, matching cidrConflictSuite's TearDownSuite contract), and every
// connection this test opens is explicitly quit before it returns.
func (s *Conflicts) Test_SubnetConflict() {
	t := s.T()
	ctx := s.Ctx()
	r := s.R()
	ns := s.AppNamespace()
	s.Manager()

	// Mutate: this test disturbs the default connection fixture (connects
	// under a veth-manufactured conflict), so later suites must not adopt
	// whatever it leaves memoized.
	conn := rt.Mutate(t, rt.ConnectionFixture(ns))
	awaitRouted(t, ctx, s.CLI(), ns)
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
	cidr := before.RootDaemon.Subnets[0]

	names := newVethNames()
	s.Require().NoError(vethUp(ctx, cidr, names), "veth-up")
	defer vethDown(ctx, r, cidr, names)

	// vs is the range activateProxyViaWorkloads (pkg/client/rootd/session.go)
	// replaces a conflicting subnet with when routing.VirtualSubnet isn't
	// overridden, matching cidr_conflict_test.go's s.vipSubnet.
	vs := client.DefaultVirtualSubnet().String()

	s.Run("auto_resolves_to_virtual_subnet", func() {
		t := s.T()
		conn := rt.Mutate(t, rt.ConnectionFixture(ns))
		// The first session started while the conflicting address is present
		// sometimes never routes at all; a plain reconnect reliably does.
		// This is a plain connection, so the reconnect loses no flags.
		if !routedWithin(ctx, s.CLI(), 45*time.Second) {
			rt.R().Infof("[rtest] conflicts: no routes after 45s with the conflict up, reconnecting once")
			conn = rt.Reconnect(t, ctx, ns)
		}
		awaitRouted(t, ctx, s.CLI(), ns)
		defer conn.Disconnect(t)

		var lastSubnets []string
		var lastVirtualSubnet string
		s.Eventually(func() bool {
			var st daemonStatus
			if err := s.CLI().JSON(ctx, &st, "status", "--format", "json"); err != nil {
				return false
			}
			lastSubnets = st.RootDaemon.Subnets
			lastVirtualSubnet = st.RootDaemon.VirtualSubnet
			return !slices.Contains(st.RootDaemon.Subnets, cidr) && slices.Contains(st.RootDaemon.Subnets, vs)
		}, subnetPollTimeout, subnetPollInterval,
			"conflicting subnet %s should auto-resolve to %s (last subnets %v, last virtual_subnet %q)",
			cidr, vs, lastSubnets, lastVirtualSubnet)
	})

	s.Run("allow_conflicting_subnets_keeps_raw_subnet", func() {
		t := s.T()
		conn := rt.Mutate(t, rt.ConnectionFixture(ns, rt.ConnExtraArgs("--allow-conflicting-subnets", cidr)))
		awaitRouted(t, ctx, s.CLI(), ns)
		defer conn.Disconnect(t)

		var lastSubnets []string
		s.Eventually(func() bool {
			var st daemonStatus
			if err := s.CLI().JSON(ctx, &st, "status", "--format", "json"); err != nil {
				return false
			}
			lastSubnets = st.RootDaemon.Subnets
			return slices.Contains(st.RootDaemon.Subnets, cidr)
		}, subnetPollTimeout, subnetPollInterval,
			"raw conflicting subnet %s should be kept when covered by --allow-conflicting-subnets (last subnets %v)",
			cidr, lastSubnets)
	})
}
