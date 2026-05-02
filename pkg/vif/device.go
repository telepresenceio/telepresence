package vif

import (
	"context"
	"net/netip"

	"gvisor.dev/gvisor/pkg/tcpip/stack"
)

type Device interface {
	Close() // Overrides stack.LinkEndpoint.Close. Must not return error.
	NewLinkEndpoint() (stack.LinkEndpoint, error)
	Index() uint32
	Name() string
	AddSubnet(context.Context, netip.Prefix) error
	RemoveSubnet(context.Context, netip.Prefix) error
	SetDNS(context.Context, string, netip.AddrPort, []string) (err error)
	WaitForDevice()
}

var _ Device = (*device)(nil)

// OpenTun creates a new TUN device and ensures that it is up and running.
func OpenTun(ctx context.Context) (Device, error) {
	return deviceResult(openTun(ctx))
}

func deviceResult(dev *device, err error) (Device, error) {
	if err != nil {
		return nil, err
	}
	return dev, nil
}

// AddSubnet adds a subnet to this TUN device and creates a route for that subnet which
// is associated with the device (removing the device will automatically remove the route).
func (d *device) AddSubnet(ctx context.Context, subnet netip.Prefix) (err error) {
	return d.addSubnet(ctx, subnet)
}

// Index returns the index of this device.
func (d *device) Index() uint32 {
	return d.interfaceIndex
}

// Name returns the name of this device, e.g. "tun0".
func (d *device) Name() string {
	return d.name
}

func (d *device) NewLinkEndpoint() (stack.LinkEndpoint, error) {
	return d.createLinkEndpoint()
}

// SetDNS sets the DNS configuration for the device on the windows platform.
func (d *device) SetDNS(ctx context.Context, clusterDomain string, server netip.AddrPort, domains []string) (err error) {
	return d.setDNS(ctx, clusterDomain, server, domains)
}

// RemoveSubnet removes a subnet from this TUN device and also removes the route for that subnet which
// is associated with the device.
func (d *device) RemoveSubnet(ctx context.Context, subnet netip.Prefix) (err error) {
	return d.removeSubnet(ctx, subnet)
}
