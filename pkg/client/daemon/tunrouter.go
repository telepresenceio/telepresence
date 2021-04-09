package daemon

import (
	"context"
	"net"

	"github.com/telepresenceio/telepresence/rpc/v2/daemon"
	"github.com/telepresenceio/telepresence/v2/pkg/subnet"
	"github.com/telepresenceio/telepresence/v2/pkg/tun"
)

// IPKey is cast of a net.IP. It must be created using IPKey(ip)
type IPKey string

func (k IPKey) IP() net.IP {
	return net.IP(k)
}

func (k IPKey) String() string {
	return net.IP(k).String()
}

type tunRouter struct {
	dispatcher *tun.Dispatcher
	ips        map[IPKey]struct{}
	subnets    map[string]*net.IPNet
}

func NewTunRouter(managerConfigured <-chan struct{}) (*tunRouter, error) {
	td, err := tun.OpenTun()
	if err != nil {
		return nil, err
	}
	return &tunRouter{
		dispatcher: tun.NewDispatcher(td, managerConfigured),
		ips:        make(map[IPKey]struct{}),
		subnets:    make(map[string]*net.IPNet),
	}, nil
}

// snapshot returns a copy of the current IP table.
func (t *tunRouter) snapshot() map[IPKey]struct{} {
	ips := make(map[IPKey]struct{}, len(t.ips))
	for k, v := range t.ips {
		ips[k] = v
	}
	return ips
}

func (t *tunRouter) setManagerInfo(c context.Context, info *daemon.ManagerInfo) error {
	return t.dispatcher.SetManagerInfo(c, info)
}

// ConfigureDNS configures the router's dispatch of DNS to the local DNS resolver
func (t *tunRouter) ConfigureDNS(ctx context.Context, dnsIP net.IP, dnsPort uint16, dnsLocalAddr *net.UDPAddr) error {
	return t.dispatcher.ConfigureDNS(ctx, dnsIP, dnsPort, dnsLocalAddr)
}

// flush will flush any pending rule changes that needs to be committed
func (t *tunRouter) flush(c context.Context, dnsIP net.IP) error {
	addedNets := make(map[string]*net.IPNet)
	ips := make([]net.IP, len(t.ips))
	i := 0
	for ip := range t.ips {
		ips[i] = net.IP(ip)
		i++
	}
	for _, sn := range subnet.AnalyzeIPs(ips) {
		// TODO: Figure out how networks cover each other, merge and remove as needed.
		// For now, we just have one 16-bit mask for the whole subnet
		if sn.IP.To4() != nil {
			sn = &net.IPNet{
				IP:   sn.IP,
				Mask: net.CIDRMask(16, 32),
			}
		}
		addedNets[sn.String()] = sn
	}

	droppedNets := make(map[string]*net.IPNet)
	for k, sn := range t.subnets {
		if _, ok := addedNets[k]; ok {
			delete(addedNets, k)
		} else {
			droppedNets[k] = sn
		}
	}
	if len(addedNets) > 0 {
		subnets := make([]*net.IPNet, len(addedNets))
		i = 0
		for k, sn := range addedNets {
			t.subnets[k] = sn
			if i > 0 && dnsIP != nil && sn.Contains(dnsIP) {
				// Ensure that the subnet for the DNS is placed first
				subnets[0], sn = sn, subnets[0]
			}
			subnets[i] = sn
			i++
		}
		return t.dispatcher.AddSubnets(c, subnets)
	}
	// TODO remove subnets that are no longer in use
	return nil
}

// clear the given ip. Returns true if the ip was cleared and false if not found.
func (t *tunRouter) clear(_ context.Context, ip IPKey) (found bool) {
	if _, found = t.ips[ip]; found {
		delete(t.ips, ip)
	}
	return found
}

// add the given ip. Returns true if the io was added and false if it was already present.
func (t *tunRouter) add(_ context.Context, ip IPKey) (found bool) {
	if _, found = t.ips[ip]; !found {
		t.ips[ip] = struct{}{}
	}
	return !found
}
