package routing

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"

	"golang.org/x/sys/windows"
	"golang.zx2c4.com/wireguard/windows/tunnel/winipcfg"

	"github.com/telepresenceio/telepresence/v2/pkg/iputil"
	"github.com/telepresenceio/telepresence/v2/pkg/proc"
	"github.com/telepresenceio/telepresence/v2/pkg/subnet"
)

type table struct{}

func GetRoutingTable(ctx context.Context) ([]*Route, error) {
	table, err := winipcfg.GetIPForwardTable2(windows.AF_UNSPEC)
	if err != nil {
		return nil, fmt.Errorf("unable to get routing table: %w", err)
	}
	routes := []*Route{}
	for _, row := range table {
		dst := row.DestinationPrefix.Prefix()
		if !dst.IsValid() {
			continue
		}
		gw := row.NextHop.Addr()
		if !gw.IsValid() {
			continue
		}
		ifaceIdx := int(row.InterfaceIndex)
		iface, err := net.InterfaceByIndex(ifaceIdx)
		if err != nil {
			return nil, fmt.Errorf("unable to get interface at index %d: %w", ifaceIdx, err)
		}
		localIP, err := interfaceLocalIP(iface, dst.Addr().Is4())
		if err != nil {
			return nil, err
		}
		if localIP == nil {
			continue
		}
		gwc := gw.AsSlice()
		ip := dst.Addr().AsSlice()
		var mask net.IPMask
		if dst.Bits() > 0 {
			if dst.Addr().Is4() {
				mask = net.CIDRMask(dst.Bits(), 32)
			} else {
				mask = net.CIDRMask(dst.Bits(), 128)
			}
		}
		routedNet := &net.IPNet{
			IP:   ip,
			Mask: mask,
		}
		routes = append(routes, &Route{
			LocalIP:   localIP,
			Gateway:   gwc,
			RoutedNet: routedNet,
			Interface: iface,
			Default:   subnet.IsZeroMask(routedNet),
		})
	}
	return routes, nil
}

func getRoute(ctx context.Context, routedNet *net.IPNet) (*Route, error) {
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
	cmd := proc.CommandContext(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", pshScript)
	cmd.DisableLogging = true
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("unable to run 'Find-Netroute -RemoteIPAddress %s': %w", ip, err)
	}
	lines := strings.Split(string(out), "\n")
	localIP := iputil.Parse(strings.TrimSpace(lines[0]))
	if localIP == nil {
		return nil, fmt.Errorf("unable to parse IP from %s", lines[0])
	}
	gatewayIP := iputil.Parse(strings.TrimSpace(lines[1]))
	if gatewayIP == nil {
		return nil, fmt.Errorf("unable to parse gateway IP from %s", lines[1])
	}
	interfaceIndex, err := strconv.Atoi(strings.TrimSpace(lines[2]))
	if err != nil {
		return nil, fmt.Errorf("unable to parse interface index from %s: %w", lines[2], err)
	}
	iface, err := net.InterfaceByIndex(interfaceIndex)
	if err != nil {
		return nil, fmt.Errorf("unable to get interface for index %d: %w", interfaceIndex, err)
	}
	return &Route{
		LocalIP:   localIP,
		Gateway:   gatewayIP,
		Interface: iface,
		RoutedNet: routedNet,
	}, nil
}

func maskToIP(mask net.IPMask) (ip net.IP) {
	ip = make(net.IP, len(mask))
	copy(ip[:], mask)
	return ip
}

func (r *Route) addStatic(ctx context.Context) error {
	mask := maskToIP(r.RoutedNet.Mask)
	cmd := proc.CommandContext(ctx,
		"route",
		"ADD",
		r.RoutedNet.IP.String(),
		"MASK",
		mask.String(),
		r.Gateway.String(),
	)
	cmd.DisableLogging = true
	out, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("failed to create route %s: %w", r, err)
	}
	if !strings.Contains(string(out), "OK!") {
		return fmt.Errorf("failed to create route %s: %s", r, strings.TrimSpace(string(out)))
	}
	return nil
}

func (r *Route) removeStatic(ctx context.Context) error {
	cmd := proc.CommandContext(ctx,
		"route",
		"DELETE",
		r.RoutedNet.IP.String(),
	)
	cmd.DisableLogging = true
	err := cmd.Run()
	if err != nil {
		return fmt.Errorf("failed to delete route %s: %w", r, err)
	}
	return nil
}

func openTable(ctx context.Context) (Table, error) {
	return &table{}, nil
}

func (t *table) Add(ctx context.Context, r *Route) error {
	return r.AddStatic(ctx)
}

func (t *table) Remove(ctx context.Context, r *Route) error {
	return r.RemoveStatic(ctx)
}

func (t *table) Close(ctx context.Context) error {
	return nil
}

func osCompareRoutes(ctx context.Context, osRoute, tableRoute *Route) (bool, error) {
	return false, nil
}
