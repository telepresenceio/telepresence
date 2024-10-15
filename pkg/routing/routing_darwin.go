package routing

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"os"
	"regexp"

	"golang.org/x/net/route"
	"golang.org/x/sys/unix"

	"github.com/datawire/dlib/dexec"
	"github.com/telepresenceio/telepresence/v2/pkg/iputil"
)

const (
	findInterfaceRegex = "(?:gateway:\\s+([0-9.]+)\\s+.*)?interface:\\s+([a-z0-9]+)"
	defaultRegex       = "destination:\\s+default"
	maskRegex          = "mask:\\s+([0-9.]+)"
)

var (
	findInterfaceRe = regexp.MustCompile(findInterfaceRegex)
	defaultRe       = regexp.MustCompile(defaultRegex)
	maskRe          = regexp.MustCompile(maskRegex)
)

func getConsistentRoutingTable(ctx context.Context) ([]*Route, error) {
	b, err := route.FetchRIB(unix.AF_UNSPEC, route.RIBTypeRoute, 0)
	if err != nil {
		return nil, err
	}
	msgs, err := route.ParseRIB(route.RIBTypeRoute, b)
	if err != nil {
		return nil, err
	}
	routes := []*Route{}
	for _, msg := range msgs {
		rm := msg.(*route.RouteMessage)
		if rm.Flags&unix.RTF_UP == 0 {
			continue
		}
		dst, gw, mask := rm.Addrs[unix.RTAX_DST], rm.Addrs[unix.RTAX_GATEWAY], rm.Addrs[unix.RTAX_NETMASK]
		if dst == nil || gw == nil || mask == nil {
			continue
		}
		iface, err := net.InterfaceByIndex(rm.Index)
		if err != nil {
			// This is not an atomic operation. An interface may vanish while we're iterating the RIB. When that
			// happens, the best cause of action is to redo the whole process.
			return nil, errInconsistentRT
		}
		if iface.Flags&net.FlagUp == 0 {
			continue
		}
		switch a := dst.(type) {
		case *route.Inet4Addr:
			localIP, err := interfaceLocalIP(iface, true)
			if err != nil {
				return nil, err
			}
			if !localIP.IsValid() {
				continue
			}
			mask, ok := mask.(*route.Inet4Addr)
			if !ok {
				continue
			}
			var gwIP netip.Addr
			if gwAddr, ok := gw.(*route.Inet4Addr); ok {
				gwIP = netip.AddrFrom4(gwAddr.IP)
			}
			ip4Mask := net.IPv4Mask(mask.IP[0], mask.IP[1], mask.IP[2], mask.IP[3])
			ones, _ := ip4Mask.Size()
			routedNet := netip.PrefixFrom(netip.AddrFrom4(a.IP), ones)
			routes = append(routes, &Route{
				Interface: iface,
				Gateway:   gwIP,
				LocalIP:   localIP,
				RoutedNet: routedNet,
				Default:   ones == 0,
			})
		case *route.Inet6Addr:
			localIP, err := interfaceLocalIP(iface, false)
			if err != nil {
				return nil, err
			}
			if !localIP.IsValid() {
				continue
			}
			mask, ok := mask.(*route.Inet6Addr)
			if !ok {
				continue
			}
			var gwIP netip.Addr
			if gwAddr, ok := gw.(*route.Inet6Addr); ok {
				gwIP = netip.AddrFrom16(gwAddr.IP)
			}
			i := 0
			for _, b := range mask.IP {
				if b == 0 {
					break
				}
				i++
			}
			routedNet := netip.PrefixFrom(netip.AddrFrom16(a.IP), i*8)
			routes = append(routes, &Route{
				Interface: iface,
				Gateway:   gwIP,
				LocalIP:   localIP,
				RoutedNet: routedNet,
				Default:   i == 0,
			})
		}
	}
	return routes, nil
}

func getOsRoute(ctx context.Context, routedNet netip.Prefix) (*Route, error) {
	ip := routedNet.Addr()
	cmd := dexec.CommandContext(ctx, "route", "-n", "get", ip.String())
	cmd.DisableLogging = true
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("unable to run 'route -n get %s': %w", ip, err)
	}
	match := findInterfaceRe.FindStringSubmatch(string(out))
	// This might fail because no "gateway" is listed. The problem is that without a gateway IP we can't
	// route to the network anyway, so we should just return an error.
	if match == nil {
		return nil, fmt.Errorf("%s did not match output of route:\n%s", findInterfaceRegex, out)
	}
	ifaceName := match[2]
	iface, err := net.InterfaceByName(ifaceName)
	if err != nil {
		return nil, fmt.Errorf("unable to get interface object for interface %s: %w", ifaceName, err)
	}
	var gatewayIp netip.Addr
	if gateway := match[1]; gateway != "" {
		gatewayIp, err = netip.ParseAddr(gateway)
		if err != nil {
			return nil, fmt.Errorf("unable to parse gateway %s: %v", gateway, err)
		}
	}
	localIP, err := interfaceLocalIP(iface, ip.Is4())
	if err != nil {
		return nil, err
	}
	ones := routedNet.Bits()
	if match := maskRe.FindStringSubmatch(string(out)); match != nil {
		ip := iputil.Parse(match[1])
		mask := net.IPv4Mask(ip[0], ip[1], ip[2], ip[3])
		ones, _ = mask.Size()
	}
	routed := netip.PrefixFrom(ip, ones)
	isDefault := false
	if match := defaultRe.FindStringSubmatch(string(out)); match != nil {
		isDefault = true
	}
	isDefault = isDefault || ones == 0
	return &Route{
		RoutedNet: routed,
		LocalIP:   localIP,
		Interface: iface,
		Gateway:   gatewayIp,
		Default:   isDefault,
	}, nil
}

// withRouteSocket will open the socket to where RouteMessages should be sent
// and call the given function with that socket. The socket is closed when the
// function returns.
func withRouteSocket(f func(routeSocket int) error) error {
	routeSocket, err := unix.Socket(unix.AF_ROUTE, unix.SOCK_RAW, unix.AF_UNSPEC)
	if err != nil {
		return err
	}

	// Avoid the overhead of echoing messages back to sender
	if err = unix.SetsockoptInt(routeSocket, unix.SOL_SOCKET, unix.SO_USELOOPBACK, 0); err != nil {
		return err
	}
	defer unix.Close(routeSocket)
	return f(routeSocket)
}

// toRouteAddr converts a net.IP to its corresponding addrMessage.Addr.
func toRouteAddr(ip netip.Addr) (addr route.Addr) {
	if ip.Is4() {
		return &route.Inet4Addr{IP: ip.As4()}
	}
	return &route.Inet6Addr{IP: ip.As16()}
}

func toRoute4Mask(bits int) (addr route.Addr) {
	mask := net.CIDRMask(bits, 32)
	dst := route.Inet4Addr{}
	copy(dst.IP[:], mask)
	return &dst
}

func toRoute6Mask(bits int) (addr route.Addr) {
	mask := net.CIDRMask(bits, 128)
	dst := route.Inet6Addr{}
	copy(dst.IP[:], mask)
	return &dst
}

func newRouteMessage(rtm, seq int, subnet netip.Prefix, gw netip.Addr) *route.RouteMessage {
	var mask route.Addr
	if subnet.Addr().Is4() {
		mask = toRoute4Mask(subnet.Bits())
	} else {
		mask = toRoute6Mask(subnet.Bits())
	}
	return &route.RouteMessage{
		Version: unix.RTM_VERSION,
		ID:      uintptr(os.Getpid()),
		Seq:     seq,
		Type:    rtm,
		Flags:   unix.RTF_UP | unix.RTF_STATIC | unix.RTF_CLONING | unix.RTF_GATEWAY,
		Addrs: []route.Addr{
			unix.RTAX_DST:     toRouteAddr(subnet.Addr()),
			unix.RTAX_GATEWAY: toRouteAddr(gw),
			unix.RTAX_NETMASK: mask,
		},
	}
}

func Add(seq int, r netip.Prefix, gw netip.Addr) error {
	return withRouteSocket(func(routeSocket int) error {
		m := newRouteMessage(unix.RTM_ADD, seq, r, gw)
		wb, err := m.Marshal()
		if err != nil {
			return err
		}
		_, err = unix.Write(routeSocket, wb)
		if err == unix.EEXIST {
			// route exists, that's OK
			err = nil
		}
		return err
	})
}

func Clear(seq int, r netip.Prefix, gw netip.Addr) error {
	return withRouteSocket(func(routeSocket int) error {
		m := newRouteMessage(unix.RTM_DELETE, seq, r, gw)
		wb, err := m.Marshal()
		if err != nil {
			return err
		}
		_, err = unix.Write(routeSocket, wb)
		if err == unix.ESRCH {
			// addrMessage doesn't exist, that's OK
			err = nil
		}
		return err
	})
}

func (r *Route) addStatic(ctx context.Context) error {
	return Add(1, r.RoutedNet, r.Gateway)
}

func (r *Route) removeStatic(ctx context.Context) error {
	return Clear(1, r.RoutedNet, r.Gateway)
}

type table struct{}

func openTable(ctx context.Context) (Table, error) {
	return &table{}, nil
}

func (t *table) Close(ctx context.Context) error {
	return nil
}

func (t *table) Add(ctx context.Context, r *Route) error {
	return r.AddStatic(ctx)
}

func (t *table) Remove(ctx context.Context, r *Route) error {
	return r.RemoveStatic(ctx)
}

func osCompareRoutes(ctx context.Context, osRoute, tableRoute *Route) (bool, error) {
	return false, nil
}
