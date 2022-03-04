package routing

import (
	"context"
	"errors"
	"fmt"
	"net"
)

type Route struct {
	LocalIP   net.IP
	RoutedNet *net.IPNet
	Interface *net.Interface
	Gateway   net.IP
	Default   bool
}

func DefaultRoute(ctx context.Context) (Route, error) {
	rt, err := GetRoutingTable(ctx)
	if err != nil {
		return Route{}, err
	}
	for _, r := range rt {
		if r.Default {
			return r, nil
		}
	}
	return Route{}, errors.New("unable to find a default route")
}

func (r *Route) Routes(ip net.IP) bool {
	return r.RoutedNet.Contains(ip)
}

func (r Route) String() string {
	if r.Default {
		return fmt.Sprintf("default via %s dev %s, gw %s", r.LocalIP, r.Interface.Name, r.Gateway)
	}
	return fmt.Sprintf("%s via %s dev %s, gw %s", r.RoutedNet, r.LocalIP, r.Interface.Name, r.Gateway)
}

func interfaceLocalIP(iface *net.Interface, ipv4 bool) (net.IP, error) {
	addrs, err := iface.Addrs()
	if err != nil {
		return net.IP{}, fmt.Errorf("unable to get interface addresses for interface %s: %w", iface.Name, err)
	}
	for _, addr := range addrs {
		ip, _, err := net.ParseCIDR(addr.String())
		if err != nil {
			return net.IP{}, fmt.Errorf("unable to parse address %s: %v", addr.String(), err)
		}
		if ip.To4() != nil {
			if ipv4 {
				return ip.To4(), nil
			}
		} else if ipv4 {
			continue
		}
		return ip, nil
	}
	return net.IP{}, fmt.Errorf("interface %s has no addresses", iface.Name)
}
