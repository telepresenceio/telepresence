package routing

import (
	"context"
	"fmt"
	"net"
	"regexp"

	"github.com/datawire/dlib/dexec"
)

const findInterfaceRegex = "[0-9.]+ via ([0-9.]+) dev ([a-z0-9]+) src ([0-9.]+)"

var findInterfaceRe = regexp.MustCompile(findInterfaceRegex)

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
	gatewayIP := net.ParseIP(match[1])
	if gatewayIP == nil {
		return Route{}, fmt.Errorf("unable to parse gateway IP %s", match[1])
	}
	iface, err := net.InterfaceByName(match[2])
	if err != nil {
		return Route{}, fmt.Errorf("unable to get interface %s: %w", match[2], err)
	}
	localIP := net.ParseIP(match[3])
	if localIP == nil {
		return Route{}, fmt.Errorf("unable to parse local IP %s", match[3])
	}
	return Route{
		Gateway:   gatewayIP,
		Interface: iface,
		RoutedNet: routedNet,
		LocalIP:   localIP,
	}, nil
}
