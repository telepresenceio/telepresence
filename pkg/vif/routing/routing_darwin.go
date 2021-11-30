package routing

import (
	"context"
	"fmt"
	"net"
	"regexp"

	"golang.org/x/net/route"
	"golang.org/x/sys/unix"

	"github.com/datawire/dlib/dexec"
	"github.com/telepresenceio/telepresence/v2/pkg/iputil"
)

const findInterfaceRegex = "gateway:\\s+([0-9.]+)\\s+.*interface:\\s+([a-z0-9]+)"

var findInterfaceRe = regexp.MustCompile(findInterfaceRegex)

func GetRoutingTable(ctx context.Context) ([]Route, error) {
	b, err := route.FetchRIB(unix.AF_UNSPEC, route.RIBTypeRoute, 0)
	if err != nil {
		return nil, err
	}
	msgs, err := route.ParseRIB(route.RIBTypeRoute, b)
	if err != nil {
		return nil, err
	}
	routes := []Route{}
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
			mask, ok := mask.(*route.Inet4Addr)
			if !ok {
				continue
			}
			gw, ok := gw.(*route.Inet4Addr)
			if !ok {
				continue
			}
			routes = append(routes, Route{
				Interface: iface,
				Gateway:   net.IP(gw.IP[:]),
				LocalIP:   localIP,
				RoutedNet: &net.IPNet{
					IP:   net.IP(a.IP[:]),
					Mask: net.IPv4Mask(mask.IP[0], mask.IP[1], mask.IP[2], mask.IP[3]),
				},
			})
		case *route.Inet6Addr:
			localIP, err := interfaceLocalIP(iface, false)
			if err != nil {
				return nil, err
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
			routes = append(routes, Route{
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

func GetRoute(ctx context.Context, routedNet *net.IPNet) (Route, error) {
	ip := routedNet.IP
	cmd := dexec.CommandContext(ctx, "route", "-n", "get", ip.String())
	cmd.DisableLogging = true
	out, err := cmd.Output()
	if err != nil {
		return Route{}, fmt.Errorf("unable to run 'route -n get %s': %w", ip, err)
	}
	match := findInterfaceRe.FindStringSubmatch(string(out))
	// This might fail because no "gateway" is listed. The problem is that without a gateway IP we can't
	// route to the network anyway, so we should just return an error.
	if match == nil {
		return Route{}, fmt.Errorf("%s did not match output of route:\n%s", findInterfaceRegex, out)
	}
	ifaceName := match[2]
	iface, err := net.InterfaceByName(ifaceName)
	if err != nil {
		return Route{}, fmt.Errorf("unable to get interface object for interface %s: %w", ifaceName, err)
	}
	gateway := match[1]
	gatewayIp := iputil.Parse(gateway)
	if gatewayIp == nil {
		return Route{}, fmt.Errorf("unable to parse gateway %s", gateway)
	}
	localIP, err := interfaceLocalIP(iface, ip.To4() != nil)
	if err != nil {
		return Route{}, err
	}
	return Route{
		RoutedNet: routedNet,
		LocalIP:   localIP,
		Interface: iface,
		Gateway:   gatewayIp,
	}, nil
}
