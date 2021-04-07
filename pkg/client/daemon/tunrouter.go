package daemon

import (
	"context"
	"net"

	"github.com/telepresenceio/telepresence/rpc/v2/daemon"
	"github.com/telepresenceio/telepresence/v2/pkg/client/daemon/nat"
	"github.com/telepresenceio/telepresence/v2/pkg/subnet"
	"github.com/telepresenceio/telepresence/v2/pkg/tun"
)

type Router interface {
	// Flush will flush any pending rule changes that needs to be committed
	Flush(ctx context.Context, dnsIP net.IP) error

	// Clear the given route. Returns true if the route was cleared and  false if no such route was found.
	Clear(ctx context.Context, route *nat.Route) (bool, error)

	// Add the given route. If the route already exists and is different from the given route, it is
	// cleared before the new route is added. Returns true if the route was add and false if it was already present.
	Add(ctx context.Context, route *nat.Route) (bool, error)

	// Disable the router.
	Disable(ctx context.Context) error

	// Enable the router
	Enable(ctx context.Context) error

	// Device returns the TUN device
	Device() *tun.Device

	// ConfigureDNS configures the router's dispatch of DNS to the local DNS resolver
	ConfigureDNS(ctx context.Context, dnsIP net.IP, dnsPort uint16, dnsLocalAddr *net.UDPAddr) error
}

type tunRouter struct {
	dispatcher *tun.Dispatcher
	ips        map[string]net.IP
	subnets    map[string]*net.IPNet
}

func NewTunRouter(managerConfigured <-chan struct{}) (Router, error) {
	td, err := tun.OpenTun()
	if err != nil {
		return nil, err
	}
	return &tunRouter{
		dispatcher: tun.NewDispatcher(td, managerConfigured),
		ips:        make(map[string]net.IP),
		subnets:    make(map[string]*net.IPNet),
	}, nil
}

func (t *tunRouter) SetManagerInfo(c context.Context, info *daemon.ManagerInfo) error {
	return t.dispatcher.SetManagerInfo(c, info)
}

func (t *tunRouter) ConfigureDNS(ctx context.Context, dnsIP net.IP, dnsPort uint16, dnsLocalAddr *net.UDPAddr) error {
	return t.dispatcher.ConfigureDNS(ctx, dnsIP, dnsPort, dnsLocalAddr)
}

func (t *tunRouter) Flush(c context.Context, dnsIP net.IP) error {
	addedNets := make(map[string]*net.IPNet)
	ips := make([]net.IP, len(t.ips))
	i := 0
	for _, ip := range t.ips {
		ips[i] = ip
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
				first := subnets[0]
				subnets[0] = sn
				sn = first
			}
			subnets[i] = sn
			i++
		}
		return t.dispatcher.AddSubnets(c, subnets)
	}
	// TODO remove subnets that are no longer in use
	return nil
}

func (t *tunRouter) Clear(_ context.Context, route *nat.Route) (bool, error) {
	ip := route.IP()
	k := ip.String()
	if _, ok := t.ips[k]; ok {
		delete(t.ips, k)
		return true, nil
	}
	return false, nil
}

func (t *tunRouter) Add(_ context.Context, route *nat.Route) (bool, error) {
	ip := route.IP()
	k := ip.String()
	if _, ok := t.ips[k]; ok {
		return false, nil
	}
	t.ips[k] = ip
	return true, nil
}

func (t *tunRouter) Disable(c context.Context) error {
	t.dispatcher.Stop(c)
	return nil
}

func (t *tunRouter) Enable(c context.Context) error {
	go func() {
		_ = t.dispatcher.Run(c)
	}()
	return nil
}

func (t *tunRouter) Device() *tun.Device {
	return t.dispatcher.Device()
}
