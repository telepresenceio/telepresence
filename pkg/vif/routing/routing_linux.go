package routing

import (
	"context"
	"fmt"
	"net"
	"regexp"

	//nolint:depguard // sys/unix does not have NetlinkRIB
	"syscall"
	"unsafe"

	"github.com/datawire/dlib/dexec"
	"github.com/telepresenceio/telepresence/v2/pkg/iputil"
)

const findInterfaceRegex = "^[0-9.]+( via (?P<gw>[0-9.]+))? dev (?P<dev>[a-z0-9-]+) src (?P<src>[0-9.]+)"

var (
	findInterfaceRe = regexp.MustCompile(findInterfaceRegex)
	gwidx           = findInterfaceRe.SubexpIndex("gw")
	devIdx          = findInterfaceRe.SubexpIndex("dev")
	srcIdx          = findInterfaceRe.SubexpIndex("src")
)

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

func GetRoutingTable(ctx context.Context) ([]Route, error) {
	// Most of this logic was adapted from https://github.com/google/gopacket/blob/master/routing/routing.go
	tab, err := syscall.NetlinkRIB(syscall.RTM_GETROUTE, syscall.AF_UNSPEC)
	if err != nil {
		return nil, fmt.Errorf("unable to call netlink for route table: %w", err)
	}
	msgs, err := syscall.ParseNetlinkMessage(tab)
	if err != nil {
		return nil, fmt.Errorf("unable to parse netlink messages: %w", err)
	}
	routes := []Route{}
msgLoop:
	for _, msg := range msgs {
		switch msg.Header.Type {
		case syscall.NLMSG_DONE:
			break msgLoop
		case syscall.RTM_NEWROUTE:
			// Based on the gopacket code, we mainly need this rtmsg to grab the size of the mask for the destination network.
			rt := (*rtmsg)(unsafe.Pointer(&msg.Data[0]))
			var (
				gw       net.IP
				dstNet   *net.IPNet
				ifaceIdx int = -1
				ipv4     bool
			)
			switch rt.Family {
			case syscall.AF_INET:
				ipv4 = true
			case syscall.AF_INET6:
				ipv4 = false
			default:
				continue msgLoop
			}
			attrs, err := syscall.ParseNetlinkRouteAttr(&msg)
			if err != nil {
				return nil, fmt.Errorf("failed to parse netlink route attributes: %w", err)
			}
			for _, attr := range attrs {
				switch attr.Attr.Type {
				case syscall.RTA_DST:
					dstNet = &net.IPNet{
						IP:   net.IP(attr.Value),
						Mask: net.CIDRMask(int(rt.DstLen), len(attr.Value)*8),
					}
				case syscall.RTA_GATEWAY:
					gw = net.IP(attr.Value)
				case syscall.RTA_OIF:
					ifaceIdx = int(*(*uint32)(unsafe.Pointer(&attr.Value[0])))
				}
			}
			// Default route -- just make the dstNet 0.0.0.0
			if gw != nil && dstNet == nil {
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
			if gw != nil && dstNet != nil && ifaceIdx > 0 {
				iface, err := net.InterfaceByIndex(ifaceIdx)
				if err != nil {
					return nil, fmt.Errorf("unable to get interface at index %d: %w", ifaceIdx, err)
				}
				srcIP, err := interfaceLocalIP(iface, ipv4)
				if err != nil {
					return nil, err
				}
				routes = append(routes, Route{
					LocalIP:   srcIP,
					RoutedNet: dstNet,
					Interface: iface,
					Gateway:   gw,
				})
			}
		}
	}
	return routes, nil
}

func GetRoute(ctx context.Context, routedNet *net.IPNet) (Route, error) {
	ip := routedNet.IP
	cmd := dexec.CommandContext(ctx, "ip", "route", "get", ip.String())
	cmd.DisableLogging = true
	out, err := cmd.Output()
	if err != nil {
		return Route{}, fmt.Errorf("failed to get route for %s: %w", ip, err)
	}
	match := findInterfaceRe.FindStringSubmatch(string(out))
	if match == nil {
		return Route{}, fmt.Errorf("output of ip route did not match %s (output: %s)", findInterfaceRegex, out)
	}
	var gatewayIP net.IP
	gw := match[gwidx]
	if gw != "" {
		gatewayIP = iputil.Parse(gw)
		if gatewayIP == nil {
			return Route{}, fmt.Errorf("unable to parse gateway IP %s", gw)
		}
	}
	iface, err := net.InterfaceByName(match[devIdx])
	if err != nil {
		return Route{}, fmt.Errorf("unable to get interface %s: %w", match[devIdx], err)
	}
	localIP := iputil.Parse(match[srcIdx])
	if localIP == nil {
		return Route{}, fmt.Errorf("unable to parse local IP %s", match[srcIdx])
	}
	return Route{
		Gateway:   gatewayIP,
		Interface: iface,
		RoutedNet: routedNet,
		LocalIP:   localIP,
	}, nil
}
