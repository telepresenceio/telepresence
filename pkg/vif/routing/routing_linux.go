package routing

import (
	"context"
	"fmt"
	"net"
	"regexp"

	"github.com/datawire/dlib/dexec"
	"github.com/telepresenceio/telepresence/v2/pkg/iputil"
)

const findInterfaceRegex = "[0-9.]+( via (?P<gw>[0-9.]+))? dev (?P<dev>[a-z0-9]+) src (?P<src>[0-9.]+)"

var (
	findInterfaceRe = regexp.MustCompile(findInterfaceRegex)
	gwidx           = findInterfaceRe.SubexpIndex("gw")
	devIdx          = findInterfaceRe.SubexpIndex("dev")
	srcIdx          = findInterfaceRe.SubexpIndex("src")
)

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
