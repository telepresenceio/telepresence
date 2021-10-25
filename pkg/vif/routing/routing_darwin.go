package routing

import (
	"context"
	"fmt"
	"net"
	"regexp"

	"github.com/datawire/dlib/dexec"
	"github.com/datawire/dlib/dlog"
)

const findInterfaceRegex = "gateway:\\s+([0-9.]+)\\s+.*interface:\\s+([a-z0-9]+)"

var findInterfaceRe = regexp.MustCompile(findInterfaceRegex)

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
	addrs, err := iface.Addrs()
	if err != nil {
		return Route{}, fmt.Errorf("unable to get interface addresses for interface %s: %w", ifaceName, err)
	}
	gateway := match[1]
	gatewayIp := net.ParseIP(gateway)
	if gatewayIp == nil {
		return Route{}, fmt.Errorf("unable to parse gateway %s", gateway)
	}
	for _, addr := range addrs {
		ip, _, err := net.ParseCIDR(addr.String())
		if err != nil {
			dlog.Warnf(ctx, "unable to parse address %s: %v", addr.String(), err)
		} else {
			return Route{
				RoutedNet: routedNet,
				LocalIP:   ip,
				Interface: iface,
				Gateway:   gatewayIp,
			}, nil
		}
	}
	return Route{}, fmt.Errorf("interface %s has no local addresses; do not know how to route", ifaceName)
}
