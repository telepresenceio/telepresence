package routing

import (
	"context"
	"fmt"
	"net"
	"os"
	"regexp"

	"golang.org/x/net/route"
	"golang.org/x/sys/unix"

	"github.com/datawire/dlib/dexec"
	"github.com/telepresenceio/telepresence/v2/pkg/iputil"
)

const findInterfaceRegex = "(?:gateway:\\s+([0-9.]+)\\s+.*)?interface:\\s+([a-z0-9]+)"

var findInterfaceRe = regexp.MustCompile(findInterfaceRegex)

func GetRoutingTable(ctx context.Context) ([]*Route, error) {
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
			return nil, fmt.Errorf("unable to get interface at index %d: %w", rm.Index, err)
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
			if localIP == nil {
				continue
			}
			mask, ok := mask.(*route.Inet4Addr)
			if !ok {
				continue
			}
			gw, ok := gw.(*route.Inet4Addr)
			if !ok {
				continue
			}
			routes = append(routes, &Route{
				Interface: iface,
				Gateway:   net.IP(gw.IP[:]),
				LocalIP:   localIP,
				RoutedNet: &net.IPNet{
					IP:   net.IP(a.IP[:]),
					Mask: net.IPv4Mask(mask.IP[0], mask.IP[1], mask.IP[2], mask.IP[3]),
				},
				Default: rm.Flags&unix.RTF_IFSCOPE == 0,
			})
		case *route.Inet6Addr:
			localIP, err := interfaceLocalIP(iface, false)
			if err != nil {
				return nil, err
			}
			if localIP == nil {
				continue
			}
			mask, ok := mask.(*route.Inet6Addr)
			if !ok {
				continue
			}
			gw, ok := gw.(*route.Inet6Addr)
			if !ok {
				continue
			}
			i := 0
			for _, b := range mask.IP {
				if b == 0 {
					break
				}
				i++
			}
			routes = append(routes, &Route{
				Interface: iface,
				Gateway:   net.IP(gw.IP[:]),
				LocalIP:   localIP,
				RoutedNet: &net.IPNet{
					IP:   net.IP(a.IP[:]),
					Mask: net.CIDRMask(i*8, 128),
				},
			})
		}
	}
	return routes, nil
}

func GetRoute(ctx context.Context, routedNet *net.IPNet) (*Route, error) {
	ip := routedNet.IP
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
	var gatewayIp net.IP
	if gateway := match[1]; gateway != "" {
		gatewayIp = iputil.Parse(gateway)
		if gatewayIp == nil {
			return nil, fmt.Errorf("unable to parse gateway %s", gateway)
		}
	}
	localIP, err := interfaceLocalIP(iface, ip.To4() != nil)
	if err != nil {
		return nil, err
	}
	return &Route{
		RoutedNet: routedNet,
		LocalIP:   localIP,
		Interface: iface,
		Gateway:   gatewayIp,
	}, nil
}

// withRouteSocket will open the socket to where RouteMessages should be sent
// and call the given function with that socket. The socket is closed when the
// function returns
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

// toRouteAddr converts a net.IP to its corresponding addrMessage.Addr
func toRouteAddr(ip net.IP) (addr route.Addr) {
	if ip4 := ip.To4(); ip4 != nil {
		dst := route.Inet4Addr{}
		copy(dst.IP[:], ip4)
		addr = &dst
	} else {
		dst := route.Inet6Addr{}
		copy(dst.IP[:], ip)
		addr = &dst
	}
	return addr
}

func toRouteMask(mask net.IPMask) (addr route.Addr) {
	if _, bits := mask.Size(); bits == 32 {
		dst := route.Inet4Addr{}
		copy(dst.IP[:], mask)
		addr = &dst
	} else {
		dst := route.Inet6Addr{}
		copy(dst.IP[:], mask)
		addr = &dst
	}
	return addr
}

func newRouteMessage(rtm, seq int, subnet *net.IPNet, gw net.IP) *route.RouteMessage {
	return &route.RouteMessage{
		Version: unix.RTM_VERSION,
		ID:      uintptr(os.Getpid()),
		Seq:     seq,
		Type:    rtm,
		Flags:   unix.RTF_UP | unix.RTF_STATIC | unix.RTF_CLONING | unix.RTF_GATEWAY,
		Addrs: []route.Addr{
			unix.RTAX_DST:     toRouteAddr(subnet.IP),
			unix.RTAX_GATEWAY: toRouteAddr(gw),
			unix.RTAX_NETMASK: toRouteMask(subnet.Mask),
		},
	}
}

func Add(seq int, r *net.IPNet, gw net.IP) error {
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

func Clear(seq int, r *net.IPNet, gw net.IP) error {
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
