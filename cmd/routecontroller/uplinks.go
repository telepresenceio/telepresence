//go:build linux

package main

import (
	"context"
	"fmt"
	"slices"

	"github.com/google/nftables"
	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"

	"github.com/telepresenceio/clog"
)

// uplinks returns the names of the interfaces that carry family's default
// routes in the current network namespace.
func uplinks(ctx context.Context, family nftables.TableFamily) ([]string, error) {
	nlFamily := unix.AF_INET
	if family == nftables.TableFamilyIPv6 {
		nlFamily = unix.AF_INET6
	}
	routes, err := netlink.RouteListFiltered(nlFamily, &netlink.Route{}, netlink.RT_FILTER_DST)
	if err != nil {
		return nil, fmt.Errorf("failed to list default routes: %w", err)
	}

	var names []string
	addLink := func(index int) {
		if index == 0 {
			return
		}
		link, err := netlink.LinkByIndex(index)
		if err != nil {
			clog.Warnf(ctx, "Failed to resolve interface %d of a default route: %v", index, err)
			return
		}
		if name := link.Attrs().Name; !slices.Contains(names, name) {
			names = append(names, name)
		}
	}
	for i := range routes {
		rt := &routes[i]
		addLink(rt.LinkIndex)
		for _, nh := range rt.MultiPath {
			addLink(nh.LinkIndex)
		}
	}
	return names, nil
}
