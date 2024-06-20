package routing

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/windows"
	"golang.zx2c4.com/wireguard/windows/tunnel/winipcfg"

	"github.com/telepresenceio/telepresence/v2/pkg/iputil"
	"github.com/telepresenceio/telepresence/v2/pkg/proc"
	"github.com/telepresenceio/telepresence/v2/pkg/subnet"
)

type table struct{}

func rowAsRoute(row *winipcfg.MibIPforwardRow2, localIP net.IP) (*Route, error) {
	dst := row.DestinationPrefix.Prefix()
	if !dst.IsValid() {
		return nil, nil
	}
	gw := row.NextHop.Addr()
	if !gw.IsValid() {
		return nil, nil
	}
	ifaceIdx := int(row.InterfaceIndex)
	iface, err := net.InterfaceByIndex(ifaceIdx)
	if err != nil {
		return nil, errInconsistentRT
	}
	if len(localIP) == 0 {
		localIP, err = interfaceLocalIP(iface, dst.Addr().Is4())
		if err != nil {
			return nil, err
		}
	} else if ip4 := localIP.To4(); ip4 != nil {
		localIP = ip4
	}
	if localIP == nil {
		return nil, nil
	}
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
	return &Route{
		LocalIP:   localIP,
		Gateway:   gw.AsSlice(),
		RoutedNet: routedNet,
		Interface: iface,
		Default:   subnet.IsZeroMask(routedNet),
	}, nil
}

func getConsistentRoutingTable(ctx context.Context) ([]*Route, error) {
	table, err := winipcfg.GetIPForwardTable2(windows.AF_UNSPEC)
	if err != nil {
		return nil, fmt.Errorf("unable to get routing table: %w", err)
	}
	routes := []*Route{}
	for _, row := range table {
		r, err := rowAsRoute(&row, nil)
		if err != nil {
			return nil, err
		}
		if r != nil {
			routes = append(routes, r)
		}
	}
	return routes, nil
}

func getRouteForIP(localIP net.IP) (*Route, error) {
retryInconsistent:
	for i := 0; i < maxInconsistentRetries; i++ {
		table, err := winipcfg.GetIPForwardTable2(windows.AF_UNSPEC)
		if err != nil {
			return nil, fmt.Errorf("unable to get routing table: %w", err)
		}
		for _, row := range table {
			ifaceIdx := int(row.InterfaceIndex)
			if iface, err := net.InterfaceByIndex(ifaceIdx); err == nil && iface.Flags&net.FlagUp == net.FlagUp {
				if addrs, err := iface.Addrs(); err == nil {
					for _, addr := range addrs {
						if ip, _, err := net.ParseCIDR(addr.String()); err == nil && ip.Equal(localIP) {
							r, err := rowAsRoute(&row, ip)
							if err != nil {
								if err == errInconsistentRT {
									time.Sleep(inconsistentRetryDelay)
									continue retryInconsistent
								}
								return nil, err
							}
							if r != nil {
								return r, nil
							}
						}
					}
				}
			}
		}
		break
	}
	return nil, fmt.Errorf("unable to get interface index for IP %s", localIP.String())
}

func GetRoute(ctx context.Context, routedNet *net.IPNet) (*Route, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	ip := routedNet.IP
	cmd := proc.CommandContext(ctx, "pathping", "-n", "-h", "1", "-p", "100", "-w", "100", "-q", "1", ip.String())
	cmd.DisableLogging = true
	stderr := &strings.Builder{}
	cmd.Stderr = stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("unable to run 'pathping %s': %s (%w)", ip, stderr, err)
	}
	var localIP net.IP
	scanner := bufio.NewScanner(bytes.NewReader(out))
	ipLine := regexp.MustCompile(`^\s+0\s+(\S+)\s*$`)
	for scanner.Scan() {
		if match := ipLine.FindStringSubmatch(scanner.Text()); match != nil {
			if localIP = iputil.Parse(match[1]); localIP != nil {
				break
			}
		}
	}
	if localIP == nil {
		return nil, fmt.Errorf("unable to parse local IP from %q", string(out))
	}
	return getRouteForIP(localIP)
}

func maskToIP(mask net.IPMask) (ip net.IP) {
	ip = make(net.IP, len(mask))
	copy(ip[:], mask)
	return ip
}

func (r *Route) addStatic(ctx context.Context) error {
	cmd := proc.CommandContext(ctx,
		"route",
		"ADD",
		r.RoutedNet.IP.String(),
		"MASK",
		maskToIP(r.RoutedNet.Mask).String(),
		r.Gateway.String(),
		"IF",
		strconv.Itoa(r.Interface.Index),
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
