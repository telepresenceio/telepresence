package routing

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"net"
	"net/netip"
	"regexp"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/windows"
	"golang.zx2c4.com/wireguard/windows/tunnel/winipcfg"

	"github.com/telepresenceio/telepresence/v2/pkg/proc"
)

type table struct{}

// each call to `net.InterfaceByIndex(idx)` calls `net.Interfaces()` and takes ~10ms.
// on systems with thousands of routes (for example due to enterprise vpn), doing this for every route
// costs ~20s. Instead, we call `net.Interfaces()` once and create a map ourselves.
func interfacesByIndex() (map[int]net.Interface, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	byIndex := make(map[int]net.Interface, len(ifaces))
	for _, iface := range ifaces {
		byIndex[iface.Index] = iface
	}
	return byIndex, nil
}

func rowAsRoute(row *winipcfg.MibIPforwardRow2, localIP netip.Addr, ifaces map[int]net.Interface) (*Route, error) {
	dst := row.DestinationPrefix.Prefix()
	if !dst.IsValid() {
		return nil, nil
	}
	gw := row.NextHop.Addr()
	if !gw.IsValid() {
		return nil, nil
	}
	iface, ok := ifaces[int(row.InterfaceIndex)]
	if !ok {
		return nil, errInconsistentRT
	}
	return &Route{
		LocalIP:        localIP,
		Gateway:        gw,
		RoutedNet:      dst,
		InterfaceIndex: iface.Index,
		InterfaceName:  iface.Name,
		Default:        dst.Addr().IsUnspecified(),
	}, nil
}

func getConsistentRoutingTable(ctx context.Context) ([]*Route, error) {
	table, err := winipcfg.GetIPForwardTable2(windows.AF_UNSPEC)
	if err != nil {
		return nil, fmt.Errorf("unable to get routing table: %w", err)
	}
	ifaces, err := interfacesByIndex()
	if err != nil {
		return nil, fmt.Errorf("unable to enumerate network interfaces: %w", err)
	}
	routes := make([]*Route, 0, len(table))
	for _, row := range table {
		r, err := rowAsRoute(&row, netip.Addr{}, ifaces)
		if err != nil {
			return nil, err
		}
		if r != nil {
			routes = append(routes, r)
		}
	}
	return routes, nil
}

func getRouteForIP(localIP netip.Addr) (*Route, error) {
	table, err := winipcfg.GetIPForwardTable2(windows.AF_UNSPEC)
	if err != nil {
		return nil, fmt.Errorf("unable to get routing table: %w", err)
	}
	ifaces, err := interfacesByIndex()
	if err != nil {
		return nil, fmt.Errorf("unable to enumerate network interfaces: %w", err)
	}

	// Determine which interface indices own localIP by enumerating each
	// interface's addresses once, rather than re-doing it for every route.
	owners := make(map[int]bool)
	for idx, iface := range ifaces {
		if iface.Flags&net.FlagUp != net.FlagUp {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			if pfx, err := netip.ParsePrefix(addr.String()); err == nil && pfx.Addr() == localIP {
				owners[idx] = true
				break
			}
		}
	}

	for _, row := range table {
		if !owners[int(row.InterfaceIndex)] {
			continue
		}
		r, err := rowAsRoute(&row, localIP, ifaces)
		if err != nil {
			return nil, err
		}
		if r != nil {
			return r, nil
		}
	}
	return nil, fmt.Errorf("unable to get interface index for IP %s", localIP.String())
}

func GetRoute(ctx context.Context, routedNet netip.Prefix) (*Route, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	ip := routedNet.Addr()
	cmd := proc.CommandContext(ctx, "pathping", "-n", "-h", "1", "-p", "100", "-w", "100", "-q", "1", ip.String())
	stderr := &strings.Builder{}
	cmd.Stderr = stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("unable to run 'pathping %s': %s (%w)", ip, stderr, err)
	}
	scanner := bufio.NewScanner(bytes.NewReader(out))
	ipLine := regexp.MustCompile(`^\s+0\s+(\S+)\s*$`)
	for scanner.Scan() {
		if match := ipLine.FindStringSubmatch(scanner.Text()); match != nil {
			if localIP, err := netip.ParseAddr(match[1]); err == nil {
				return getRouteForIP(localIP)
			}
		}
	}
	return nil, fmt.Errorf("unable to parse local IP from %q", string(out))
}

func maskToIP(mask net.IPMask) (ip net.IP) {
	ip = make(net.IP, len(mask))
	copy(ip[:], mask)
	return ip
}

func (r *Route) addStatic(ctx context.Context) error {
	ip := r.RoutedNet.Addr()
	var maskSize int
	if ip.Is4() {
		maskSize = 32
	} else {
		maskSize = 128
	}
	args := []string{
		"ADD",
		ip.String(),
	}
	if r.RoutedNet.Bits() < maskSize {
		mask := net.CIDRMask(r.RoutedNet.Bits(), maskSize)
		args = append(args, "MASK", maskToIP(mask).String())
	}

	// Contrary to what the usage printout says, he gateway must be appended even if it is unspecified. If it is missing,
	// the command just prints its usage and exits with code -1.
	args = append(args, r.Gateway.String())

	args = append(args, "IF", strconv.Itoa(r.InterfaceIndex))
	cmd := proc.CommandContext(ctx, "route", args...)
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
		r.RoutedNet.Addr().String(),
	)
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
