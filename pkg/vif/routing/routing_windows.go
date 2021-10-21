package routing

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"

	"github.com/datawire/dlib/dexec"
)

func GetRoute(ctx context.Context, routedNet *net.IPNet) (Route, error) {
	ip := routedNet.IP
	pshScript := fmt.Sprintf(`
$job = Find-NetRoute -RemoteIPAddress "%s" -AsJob | Wait-Job -Timeout 30
if ($job.State -ne 'Completed') {
	throw "timed out getting route after 30 seconds."
}
$obj = $job | Receive-Job
$obj.IPAddress
$obj.NextHop
$obj.InterfaceIndex[0]
`, ip)
	cmd := dexec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", pshScript)
	cmd.DisableLogging = true
	out, err := cmd.Output()
	if err != nil {
		return Route{}, fmt.Errorf("unable to run 'Find-Netroute -RemoteIPAddress %s': %w", ip, err)
	}
	lines := strings.Split(string(out), "\n")
	localIP := net.ParseIP(strings.TrimSpace(lines[0]))
	if localIP == nil {
		return Route{}, fmt.Errorf("unable to parse IP from %s", lines[0])
	}
	gatewayIP := net.ParseIP(strings.TrimSpace(lines[1]))
	if gatewayIP == nil {
		return Route{}, fmt.Errorf("unable to parse gateway IP from %s", lines[1])
	}
	interfaceIndex, err := strconv.Atoi(strings.TrimSpace(lines[2]))
	if err != nil {
		return Route{}, fmt.Errorf("unable to parse interface index from %s: %w", lines[2], err)
	}
	iface, err := net.InterfaceByIndex(interfaceIndex)
	if err != nil {
		return Route{}, fmt.Errorf("unable to get interface for index %d: %w", interfaceIndex, err)
	}
	return Route{
		LocalIP:   localIP,
		Gateway:   gatewayIP,
		Interface: iface,
		RoutedNet: routedNet,
	}, nil
}
