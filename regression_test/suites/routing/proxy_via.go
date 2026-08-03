package routing

import (
	"net/netip"

	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/subnet"
	"github.com/telepresenceio/telepresence/v2/pkg/vif"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/managers"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/workloads"
)

// ProxyVia proves --proxy-via all=<workload> routes every cluster subnet
// through that workload's traffic-agent instead of the manager-bound
// tunnel: the workload's own service still round-trips, and every routed
// subnet in `status` lands inside the virtual subnet. The
// --proxy-via-and-mounts variant is left to the mounts area.
type ProxyVia struct {
	rt.Suite
}

func init() {
	rt.Register(&ProxyVia{}, rt.InArea("routing"), rt.NeedsManager(managers.Default))
}

// Test_AllSubnetsRouteThroughWorkload connects with --proxy-via all=<wl>. A
// non-default connection: ConnExtraArgs folds the flag into the fixture
// hash, so it is Mutate'd and explicitly quit at the end rather than left
// for a later suite to adopt (a plain default connect must not inherit a
// proxy-via session). It then checks that every routed subnet in `status`
// lands inside client.DefaultVirtualSubnet(): the same range
// activateProxyViaWorkloads draws proxy-via virtual IPs from
// (pkg/client/rootd/session.go) -- and that the workload's service still
// answers.
func (s *ProxyVia) Test_AllSubnetsRouteThroughWorkload() {
	t := s.T()
	ctx := s.Ctx()
	ns := s.AppNamespace()
	s.Manager()

	wl := s.Workload(workloads.Echo("proxy-via-wl"))

	conn := rt.Mutate(t, rt.ConnectionFixture(ns, rt.ConnExtraArgs("--proxy-via", "all="+wl.Name)))
	awaitRouted(t, ctx, s.CLI(), ns)
	defer conn.Disconnect(t)

	vs := client.DefaultVirtualSubnet()
	var lastSubnets []string
	var lastVirtualSubnet string
	s.Eventually(func() bool {
		var st daemonStatus
		if err := s.CLI().JSON(ctx, &st, "status", "--format", "json"); err != nil {
			return false
		}
		lastSubnets = st.RootDaemon.Subnets
		lastVirtualSubnet = st.RootDaemon.VirtualSubnet
		if len(st.RootDaemon.Subnets) == 0 {
			return false
		}
		// Dual-stack: IPv4 subnets translate into the IPv4 virtual range,
		// IPv6 ones into the fixed Telepresence ULA (vif.TelepresenceULA6),
		// mirroring rootd's per-family vip generators.
		for _, raw := range st.RootDaemon.Subnets {
			sn, err := netip.ParsePrefix(raw)
			if err != nil {
				return false
			}
			virt := vs
			if sn.Addr().Is6() {
				virt = vif.TelepresenceULA6
			}
			if !subnet.Covers(virt, sn) {
				return false
			}
		}
		return true
	}, subnetPollTimeout, subnetPollInterval,
		"proxy-via all should translate every routed subnet inside %s (last subnets %v, last virtual_subnet %q)",
		vs, lastSubnets, lastVirtualSubnet)

	rt.RoutedToCluster(t, wl.ServiceURL())
}
