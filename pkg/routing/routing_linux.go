package routing

import (
	"context"
	"fmt"
	"net"
	"regexp"
	"sort"
	"syscall" //nolint:depguard // sys/unix does not have NetlinkRIB
	"unsafe"

	"github.com/vishvananda/netlink"

	"github.com/datawire/dlib/dexec"
	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/pkg/iputil"
)

const findInterfaceRegex = `( via (?P<gw>[0-9a-f.:]+))?.* dev (?P<dev>[a-z0-9-]+).* src (?P<src>[0-9a-f.:]+)`

var (
	findInterfaceRe = regexp.MustCompile(findInterfaceRegex) //nolint:gochecknoglobals // constant
	gwidx           = findInterfaceRe.SubexpIndex("gw")      //nolint:gochecknoglobals // constant
	devIdx          = findInterfaceRe.SubexpIndex("dev")     //nolint:gochecknoglobals // constant
	srcIdx          = findInterfaceRe.SubexpIndex("src")     //nolint:gochecknoglobals // constant
)

type table struct {
	index int
	rule  *netlink.Rule
}

type rtmsg struct {
	// Check out https://man7.org/linux/man-pages/man7/rtnetlink.7.html for the definition of rtmsg
	Family   byte // Address family of route
	DstLen   byte // Length of destination
	SrcLen   byte // Length of source
	TOS      byte // TOS filter
	Table    byte // Routing table ID
	Protocol byte // Routing protocol
	Scope    byte
	Type     byte

	Flags uint32
}

func getConsistentRoutingTable(_ context.Context) ([]*Route, error) {
	// Most of this logic was adapted from https://github.com/google/gopacket/blob/master/routing/routing.go
	tab, err := syscall.NetlinkRIB(syscall.RTM_GETROUTE, syscall.AF_UNSPEC)
	if err != nil {
		return nil, fmt.Errorf("unable to call netlink for route table: %w", err)
	}
	msgs, err := syscall.ParseNetlinkMessage(tab)
	if err != nil {
		return nil, fmt.Errorf("unable to parse netlink messages: %w", err)
	}
	var routes []*Route
msgLoop:
	for _, msg := range msgs {
		switch msg.Header.Type {
		case syscall.NLMSG_DONE:
			break msgLoop
		case syscall.RTM_NEWROUTE:
			// Based on the gopacket code, we mainly need this rtmsg to grab the size of the mask for the destination network.
			r, err := rowAsRoute((*rtmsg)(unsafe.Pointer(&msg.Data[0])), &msg)
			if err != nil {
				return nil, err
			}
			if r != nil {
				routes = append(routes, r)
			}
		}
	}
	return routes, nil
}

func rowAsRoute(rt *rtmsg, msg *syscall.NetlinkMessage) (*Route, error) {
	ipv4 := false
	switch rt.Family {
	case syscall.AF_INET:
		ipv4 = true
	case syscall.AF_INET6:
	default:
		return nil, nil
	}
	attrs, err := syscall.ParseNetlinkRouteAttr(msg)
	if err != nil {
		return nil, fmt.Errorf("failed to parse netlink route attributes: %w", err)
	}

	var gw net.IP
	var dstNet *net.IPNet
	var ifaceIdx int
	for _, attr := range attrs {
		switch attr.Attr.Type {
		case syscall.RTA_DST:
			dstNet = &net.IPNet{
				IP:   attr.Value,
				Mask: net.CIDRMask(int(rt.DstLen), len(attr.Value)*8),
			}
		case syscall.RTA_GATEWAY:
			gw = attr.Value
		case syscall.RTA_OIF:
			ifaceIdx = int(*(*uint32)(unsafe.Pointer(&attr.Value[0])))
		}
	}
	if ifaceIdx < 1 {
		return nil, nil
	}

	dfltGw := false
	// Default route -- just make the dstNet 0.0.0.0
	if gw != nil && dstNet == nil {
		dfltGw = true
		if ipv4 {
			dstNet = &net.IPNet{
				IP:   net.IP{0, 0, 0, 0},
				Mask: net.CIDRMask(0, 32),
			}
		} else {
			dstNet = &net.IPNet{
				IP:   net.ParseIP("::"),
				Mask: net.CIDRMask(0, 128),
			}
		}
	}
	if dstNet == nil {
		return nil, nil
	}

	iface, err := net.InterfaceByIndex(ifaceIdx)
	if err != nil {
		// This is not an atomic operation. An intercept may vanish while we're creating this table. When that
		// happens, the best cause of action is to redo the whole process.
		return nil, errInconsistentRT
	}
	if iface.Flags&net.FlagUp == 0 {
		return nil, nil
	}
	srcIP, err := interfaceLocalIP(iface, ipv4)
	if err != nil || srcIP == nil {
		return nil, err
	}
	return &Route{
		LocalIP:   srcIP,
		RoutedNet: dstNet,
		Interface: iface,
		// gw might be nil here, indicating a local route, i.e. directly connected without the packets having to go through a gateway.
		Gateway: gw,
		Default: dfltGw,
	}, nil
}

func getOsRoute(ctx context.Context, routedNet *net.IPNet) (*Route, error) {
	ip := routedNet.IP
	cmd := dexec.CommandContext(ctx, "ip", "route", "get", ip.String())
	cmd.DisableLogging = true
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("failed to get route for %s: %w", ip, err)
	}
	msg := string(out)
	match := findInterfaceRe.FindStringSubmatch(msg)
	if match == nil {
		return nil, fmt.Errorf("output of ip route did not match %s (output: %s)", findInterfaceRegex, msg)
	}
	var gatewayIP net.IP
	gw := match[gwidx]
	if gw != "" {
		gatewayIP = iputil.Parse(gw)
		if gatewayIP == nil {
			return nil, fmt.Errorf("unable to parse gateway IP %s", gw)
		}
	}
	iface, err := net.InterfaceByName(match[devIdx])
	if err != nil {
		return nil, fmt.Errorf("unable to get interface %s: %w", match[devIdx], err)
	}
	localIP := iputil.Parse(match[srcIdx])
	if localIP == nil {
		return nil, fmt.Errorf("unable to parse local IP %s", match[srcIdx])
	}
	return &Route{
		Gateway:   gatewayIP,
		Interface: iface,
		RoutedNet: routedNet,
		LocalIP:   localIP,
	}, nil
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
		dlog.Tracef(ctx, "Found routing rule %+v", rule)
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
	dlog.Infof(ctx, "Creating routing table with index %d and priority %d", index, priority)
	rule := netlink.NewRule()
	rule.Table = index
	rule.Priority = priority
	rule.Family = netlink.FAMILY_V4
	if err := netlink.RuleAdd(rule); err != nil {
		return nil, fmt.Errorf("netlink.RuleAdd: %w", err)
	}
	return &table{
		index: index,
		rule:  rule,
	}, nil
}

func (t *table) routeToNetlink(route *Route) *netlink.Route {
	return &netlink.Route{
		Dst:       route.RoutedNet,
		Table:     t.index,
		LinkIndex: route.Interface.Index,
		Gw:        route.Gateway,
		Src:       route.LocalIP,
	}
}

func (t *table) Close(ctx context.Context) error {
	return netlink.RuleDel(t.rule)
}

func (t *table) Add(ctx context.Context, r *Route) error {
	route := t.routeToNetlink(r)
	if err := netlink.RouteAdd(route); err != nil {
		return fmt.Errorf("netlink.RouteAdd: %w", err)
	}
	return nil
}

func (t *table) Remove(ctx context.Context, r *Route) error {
	route := t.routeToNetlink(r)
	if err := netlink.RouteDel(route); err != nil {
		return fmt.Errorf("netlink.RouteDel: %w", err)
	}
	return nil
}

func (r *Route) addStatic(ctx context.Context) error {
	return dexec.CommandContext(ctx, "ip", "route", "add", r.RoutedNet.String(), "via", r.Gateway.String(), "dev", r.Interface.Name).Run()
}

func (r *Route) removeStatic(ctx context.Context) error {
	return dexec.CommandContext(ctx, "ip", "route", "del", r.RoutedNet.String(), "via", r.Gateway.String(), "dev", r.Interface.Name).Run()
}

func osCompareRoutes(ctx context.Context, osRoute, tableRoute *Route) (bool, error) {
	// On Linux, when we ask about an IP address assigned to the machine, the OS will give us a loopback route
	if osRoute.LocalIP.Equal(osRoute.RoutedNet.IP) && osRoute.Interface.Flags&net.FlagLoopback != 0 {
		addrs, err := tableRoute.Interface.Addrs()
		if err != nil {
			return false, err
		}
		for _, addr := range addrs {
			dlog.Tracef(ctx, "Checking address %s against %s", addr.String(), osRoute.RoutedNet.IP.String())
			if addr.(*net.IPNet).IP.Equal(osRoute.LocalIP) {
				return true, nil
			}
		}
	}
	return false, nil
}
