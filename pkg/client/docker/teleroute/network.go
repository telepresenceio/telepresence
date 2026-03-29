package teleroute

import (
	"context"
	"encoding/binary"
	"errors"
	"net/netip"
	"strconv"
	"strings"

	"github.com/containerd/errdefs"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/network"
	dockerClient "github.com/docker/docker/client"

	"github.com/telepresenceio/clog"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/daemon"
	"github.com/telepresenceio/telepresence/v2/pkg/client/docker"
	"github.com/telepresenceio/telepresence/v2/pkg/errcat"
)

const daemonLabel = "telepresence.io/teleroute/daemon"

func CreateNetwork(
	ctx context.Context, info *daemon.Info, cli *dockerClient.Client,
	teleroutePlugin string, teleroutePort uint16, clusterSubnets []netip.Prefix,
) error {
	cn := info.Name
	dockerCfg := client.GetConfig(ctx).Docker()
	ipv4 := dockerCfg.EnableIPv4
	ipv6, err := docker.UseIPv6(ctx)
	if err != nil {
		return err
	}
	host := info.ContainerIP
	if ipv6 && !ipv4 && host.Is4() {
		host = netip.AddrFrom16(host.As16())
	}
	if !ipv4 && !ipv6 {
		return errcat.User.New("unable to create teleroute network because both the IPv4 and IPv6 families are disabled")
	}
	opts := network.CreateOptions{
		Driver:     teleroutePlugin,
		Scope:      "local",
		Internal:   true,
		EnableIPv4: &ipv4,
		EnableIPv6: &ipv6,
		Options: map[string]string{
			"host": host.String(),
			"port": strconv.Itoa(int(teleroutePort)),
		},
		Labels: map[string]string{
			daemonLabel: info.ContainerID,
		},
	}
	if len(clusterSubnets) > 0 {
		subnet, findErr := FindNonConflictingSubnet(ctx, cli, clusterSubnets)
		if findErr != nil {
			clog.Warnf(ctx, "Unable to pre-compute a non-conflicting subnet: %v. Falling back to Docker default IPAM", findErr)
		} else {
			clog.Debugf(ctx, "Using pre-computed subnet %s for teleroute network %s", subnet, cn)
			opts.IPAM = &network.IPAM{
				Config: []network.IPAMConfig{{
					Subnet: subnet.String(),
				}},
			}
		}
	}
	clog.Debugf(ctx, "Creating teleroute network %s", cn)
	rsp, err := cli.NetworkCreate(ctx, cn, opts)
	if err != nil {
		return err
	}
	if rsp.Warning != "" {
		clog.Warn(ctx, rsp.Warning)
	} else {
		clog.Debugf(ctx, "Network %s created", cn)

		// The daemon must be the first container to join the network. This join is special in that
		// it will not receive the routes that the daemon makes available. The connect is necessary
		// to open up for the daemon to communicate with other connected containers.
		err = cli.NetworkConnect(ctx, rsp.ID, info.ContainerID, &network.EndpointSettings{
			DriverOpts: map[string]string{
				"daemon": "true",
			},
		})
	}
	return err
}

// IsTelerouteNetwork returns true if the network was created by the teleroute driver.
func IsTelerouteNetwork(ctx context.Context, cli *dockerClient.Client, name string) bool {
	ni, err := cli.NetworkInspect(ctx, name, network.InspectOptions{})
	if err != nil {
		return false
	}
	_, ok := ni.Labels[daemonLabel]
	return ok
}

// NetworkGC deletes zombie teleroute networks. A teleroute network is considered a zombie when:
//
//  1. It is not connected to a Telepresence daemon
//  2. No container is currently connected to it.
func NetworkGC(ctx context.Context, cli *dockerClient.Client) error {
	ns, err := cli.NetworkList(ctx, network.ListOptions{Filters: filters.NewArgs(filters.KeyValuePair{
		Key:   "label",
		Value: daemonLabel,
	})})
	if err != nil {
		return err
	}

	for _, n := range ns {
		_, err := cli.ContainerInspect(ctx, n.Labels[daemonLabel])
		if errdefs.IsNotFound(err) {
			clog.Debugf(ctx, "Garbage collecting network %s", n.Name)
			err = cli.NetworkRemove(ctx, n.ID)
			if err != nil {
				ee := err.Error()
				if ix := strings.Index(ee, "has active endpoints"); ix > 0 {
					clog.Debugf(ctx, "Network %s was not garbage collected. It %s", n.ID, ee[ix:])
				}
			}
		}
	}
	return nil
}

// RemoveNetwork disconnects all containers that are connected to the network then removes the network.
func RemoveNetwork(ctx context.Context, cli *dockerClient.Client, name string) (disconnected []string, err error) {
	ni, err := cli.NetworkInspect(ctx, name, network.InspectOptions{})
	if err != nil {
		return nil, err
	}

	if nc := len(ni.Containers); nc > 0 {
		clog.Debugf(ctx, "Disconnecting %d containers from network %s", nc, name)
		disconnected = make([]string, 0, nc)
		for cid := range ni.Containers {
			err = cli.NetworkDisconnect(ctx, name, cid, true)
			if err != nil {
				clog.Error(ctx, err)
			} else {
				disconnected = append(disconnected, cid)
			}
		}
	}

	clog.Debugf(ctx, "Removing network %s", name)
	return disconnected, cli.NetworkRemove(ctx, name)
}

func ReconnectNetwork(ctx context.Context, cli *dockerClient.Client, name string, disconnected []string) {
	if nc := len(disconnected); nc > 0 {
		clog.Debugf(ctx, "Reconnecting %d containers to network %s", nc, name)
		for _, cid := range disconnected {
			err := cli.NetworkConnect(ctx, name, cid, &network.EndpointSettings{})
			if err != nil {
				clog.Error(ctx, err)
			}
		}
	}
}

// FindNonConflictingSubnet finds a subnet that doesn't overlap with any existing
// Docker network or the given cluster CIDRs.
func FindNonConflictingSubnet(ctx context.Context, cli *dockerClient.Client, clusterSubnets []netip.Prefix) (netip.Prefix, error) {
	avoid := append([]netip.Prefix{}, clusterSubnets...)
	nets, err := cli.NetworkList(ctx, network.ListOptions{})
	if err != nil {
		return netip.Prefix{}, err
	}
	for _, n := range nets {
		for _, cfg := range n.IPAM.Config {
			if p, err := netip.ParsePrefix(cfg.Subnet); err == nil {
				avoid = append(avoid, p)
			}
		}
	}
	return findFreeSubnet(avoid)
}

// findFreeSubnet returns the first RFC 1918 subnet that doesn't overlap with any prefix in avoid.
// It tries progressively smaller subnets across all three RFC 1918 ranges.
func findFreeSubnet(avoid []netip.Prefix) (netip.Prefix, error) {
	type pool struct {
		start netip.Addr
		bits  int
		count int
	}
	pools := []pool{
		{netip.MustParseAddr("172.16.0.0"), 16, 16},   // 172.16–31.0.0/16
		{netip.MustParseAddr("10.0.0.0"), 20, 4096},   // 10.0.0.0/8 as /20s
		{netip.MustParseAddr("10.0.0.0"), 24, 65536},  // 10.0.0.0/8 as /24s
		{netip.MustParseAddr("192.168.0.0"), 24, 256}, // 192.168.0.0/16 as /24s
	}
	for _, pl := range pools {
		p := netip.PrefixFrom(pl.start, pl.bits)
		for range pl.count {
			if !overlapsAny(p, avoid) {
				return p, nil
			}
			p = nextPrefix(p)
		}
	}
	return netip.Prefix{}, errors.New("no non-conflicting RFC 1918 subnet found")
}

// overlapsAny returns true if subnet overlaps with any prefix in cidrs.
func overlapsAny(subnet netip.Prefix, cidrs []netip.Prefix) bool {
	for _, c := range cidrs {
		if subnet.Overlaps(c) {
			return true
		}
	}
	return false
}

// nextPrefix returns the prefix of the same size immediately following p.
// For example, nextPrefix("172.16.0.0/16") returns "172.17.0.0/16".
func nextPrefix(p netip.Prefix) netip.Prefix {
	// Convert the 4-byte IP address to a single integer so we can do arithmetic on it.
	b := p.Addr().As4()
	ip := binary.BigEndian.Uint32(b[:])

	// A /16 has 65536 addresses, a /20 has 4096, a /24 has 256, etc.
	subnetSize := uint32(1) << (32 - p.Bits())

	next := ip + subnetSize
	if next < ip {
		// Wrapped past 255.255.255.255.
		return netip.Prefix{}
	}

	// Convert back from integer to 4-byte IP address.
	var nb [4]byte
	binary.BigEndian.PutUint32(nb[:], next)
	return netip.PrefixFrom(netip.AddrFrom4(nb), p.Bits())
}
