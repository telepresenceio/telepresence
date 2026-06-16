package routing

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"os/exec"
	"sort"
	"syscall" //nolint:depguard // sys/unix does not have NetlinkRIB

	"github.com/vishvananda/netlink"

	"github.com/telepresenceio/clog"
	"github.com/telepresenceio/telepresence/v2/pkg/iputil"
	"github.com/telepresenceio/telepresence/v2/pkg/subnet"
)

type LinuxTable interface {
	RouteToNetlink(route *Route) *netlink.Route
}

type table struct {
	index int
	rules []*netlink.Rule
}

func getConsistentRoutingTable(ctx context.Context) ([]*Route, error) {
	// List routes in the all tables.
	rts, err := netlink.RouteListFiltered(
		netlink.FAMILY_ALL,
		&netlink.Route{},
		netlink.RT_FILTER_TABLE,
	)
	if err != nil {
		return nil, fmt.Errorf("netlink.RouteListFiltered: %w", err)
	}

	var routes []*Route
	for i := range rts {
		nrt := &rts[i]
		switch nrt.Family {
		case syscall.AF_INET, syscall.AF_INET6:
			if shouldSkipNetlinkRoute(nrt) {
				clog.Tracef(ctx, "Skipping non-interface route %+v", nrt)
				continue
			}
			rt, err := routeFromNetlinkRoute(nrt)
			if err != nil {
				// The route references an interface that no longer exists — a device that
				// vanished mid-iteration, or a stale entry in a leaked policy table. Skip
				// it rather than discarding the whole snapshot. Telepresence routes cluster
				// subnets through its own policy table (index >= 775), so unlike before we
				// must include tables above the system range rather than skipping them.
				clog.Tracef(ctx, "Skipping unresolvable route %+v: %v", nrt, err)
				continue
			}
			clog.Tracef(ctx, "Found route %s", rt)
			routes = append(routes, rt)
		}
	}
	return routes, nil
}

func shouldSkipNetlinkRoute(rt *netlink.Route) bool {
	switch rt.Type {
	case syscall.RTN_BLACKHOLE, syscall.RTN_UNREACHABLE, syscall.RTN_PROHIBIT, syscall.RTN_THROW:
		return true
	}
	return rt.LinkIndex == 0
}

func routeFromNetlinkRoute(rt *netlink.Route) (*Route, error) {
	lnk, err := netlink.LinkByIndex(rt.LinkIndex)
	if err != nil {
		return nil, fmt.Errorf("netlink.LinkByIndex: %w", err)
	}
	addr, _ := netip.AddrFromSlice(rt.Src)
	gw, _ := netip.AddrFromSlice(rt.Gw)
	dst := iputil.PrefixFromIPNet(rt.Dst)
	dfltGw := gw.IsValid() && dst.Addr().IsUnspecified()
	return &Route{
		InterfaceIndex: rt.LinkIndex,
		InterfaceName:  lnk.Attrs().Name,
		LocalIP:        addr,
		RoutedNet:      iputil.PrefixFromIPNet(rt.Dst),
		Gateway:        gw,
		Default:        dfltGw,
	}, nil
}

func getOsRoute(ctx context.Context, routedNet netip.Prefix) (*Route, error) {
	nrt, err := netlink.RouteGet(routedNet.Addr().AsSlice())
	if err == nil && len(nrt) > 0 {
		return routeFromNetlinkRoute(&nrt[0])
	}
	return nil, err
}

func openTable(ctx context.Context) (Table, error) {
	rules, err := netlink.RuleList(netlink.FAMILY_ALL)
	if err != nil {
		return nil, fmt.Errorf("netlink.RuleList: %w", err)
	}
	// Sort the rules by index ascending to make sure we find an open one
	sort.Slice(rules, func(i, j int) bool {
		return rules[i].Table < rules[j].Table
	})
	index := 775
	priority := 32766 // default initial priority
	for _, rule := range rules {
		if rule.Table == 0 || rule.Table == 255 {
			// System rules, ignore
			continue
		}
		if rule.Priority <= priority {
			priority = rule.Priority - 1
		}
		if rule.Table == index {
			// There's already a table with the default index, get a new one
			index++
		}
	}
	clog.Infof(ctx, "Creating routing table with index %d and priority %d", index, priority)
	t := &table{index: index}
	// Add a policy rule per family so that both IPv4 and IPv6 cluster traffic consults
	// this table before the main table, giving Telepresence's routes deterministic
	// precedence over conflicting routes from other software (e.g. a VPN).
	for _, family := range []int{netlink.FAMILY_V4, netlink.FAMILY_V6} {
		rule := netlink.NewRule()
		rule.Table = index
		rule.Priority = priority
		rule.Family = family
		if err := netlink.RuleAdd(rule); err != nil {
			if family == netlink.FAMILY_V6 {
				// IPv6 may be disabled on the host; IPv4 routing still works without it.
				clog.Warnf(ctx, "failed to add IPv6 policy rule for table %d: %v", index, err)
				continue
			}
			return nil, fmt.Errorf("netlink.RuleAdd: %w", err)
		}
		t.rules = append(t.rules, rule)
	}
	return t, nil
}

func (t *table) RouteToNetlink(route *Route) *netlink.Route {
	nr := &netlink.Route{
		Dst:       subnet.PrefixToIPNet(route.RoutedNet),
		Table:     t.index,
		LinkIndex: route.InterfaceIndex,
	}
	// Only set the gateway and preferred source when they are actual addresses. An
	// unspecified gateway (the on-link case for our subnet routes) must be left nil:
	// passing an all-zero address is tolerated for IPv4 but rejected by the kernel with
	// EINVAL for IPv6. The same is done for the source for symmetry.
	if route.Gateway.IsValid() && !route.Gateway.IsUnspecified() {
		nr.Gw = route.Gateway.AsSlice()
	}
	if route.LocalIP.IsValid() && !route.LocalIP.IsUnspecified() {
		nr.Src = route.LocalIP.AsSlice()
	}
	return nr
}

func (t *table) Close(ctx context.Context) error {
	var err error
	for _, rule := range t.rules {
		if e := netlink.RuleDel(rule); e != nil && err == nil {
			err = e
		}
	}
	return err
}

func (t *table) Add(ctx context.Context, r *Route) error {
	route := t.RouteToNetlink(r)
	if err := netlink.RouteAdd(route); err != nil {
		return fmt.Errorf("netlink.RouteAdd: %w", err)
	}
	return nil
}

func (t *table) Remove(ctx context.Context, r *Route) error {
	route := t.RouteToNetlink(r)
	if err := netlink.RouteDel(route); err != nil {
		return fmt.Errorf("netlink.RouteDel: %w", err)
	}
	return nil
}

func (r *Route) addStatic(ctx context.Context) error {
	return exec.CommandContext(ctx, "ip", "route", "add", r.RoutedNet.String(), "via", r.Gateway.String(), "dev", r.InterfaceName).Run()
}

func (r *Route) removeStatic(ctx context.Context) error {
	return exec.CommandContext(ctx, "ip", "route", "del", r.RoutedNet.String(), "via", r.Gateway.String(), "dev", r.InterfaceName).Run()
}

func osCompareRoutes(ctx context.Context, osRoute, tableRoute *Route) (bool, error) {
	// On Linux, when we ask about an IP address assigned to the machine, the OS will give us a loopback route
	if osRoute.LocalIP == osRoute.RoutedNet.Addr() {
		osIf, err := net.InterfaceByIndex(osRoute.InterfaceIndex)
		if err != nil {
			return false, err
		}
		if osIf.Flags&net.FlagLoopback != 0 {
			tbIf, err := net.InterfaceByIndex(tableRoute.InterfaceIndex)
			if err != nil {
				return false, err
			}
			addrs, err := tbIf.Addrs()
			if err != nil {
				return false, err
			}
			for _, addr := range addrs {
				clog.Tracef(ctx, "Checking address %s against %s", addr, osRoute.RoutedNet.Addr())
				if a, ok := netip.AddrFromSlice(iputil.Normalize(addr.(*net.IPNet).IP)); ok && a == osRoute.LocalIP {
					return true, nil
				}
			}
		}
	}
	return false, nil
}
