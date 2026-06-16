package vif

import (
	"context"
	cryptoRand "crypto/rand"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"unsafe"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
	"gvisor.dev/gvisor/pkg/rawfile"
	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/link/fdbased"
	"gvisor.dev/gvisor/pkg/tcpip/stack"

	"github.com/telepresenceio/clog"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/subnet"
)

const devicePath = "/dev/net/tun"

type device struct {
	fd             int
	name           string
	endPoint       stack.LinkEndpoint
	interfaceIndex uint32
	isTAP          bool
}

func RandomMAC() net.HardwareAddr {
	addr := make([]byte, 6)
	_, _ = cryptoRand.Read(addr)
	// Clear multicast
	addr[0] &^= 1
	// Set the local bit
	addr[0] |= 2
	return addr
}

func openTun(ctx context.Context) (*device, error) {
	// https://www.kernel.org/doc/html/latest/networking/tuntap.html

	fd, err := unix.Open(devicePath, unix.O_RDWR, 0)
	if err != nil {
		return nil, fmt.Errorf("failed to open TUN device %s: %w", devicePath, err)
	}
	unix.CloseOnExec(fd)
	defer func() {
		if err != nil {
			_ = unix.Close(fd)
		}
	}()

	ifr, _ := unix.NewIfreq("tel%d")
	useTAP := client.GetConfig(ctx).Routing().UseTAP
	if useTAP {
		ifr.SetUint16(unix.IFF_TAP | unix.IFF_NO_PI)
	} else {
		ifr.SetUint16(unix.IFF_TUN | unix.IFF_NO_PI)
	}

	err = unix.IoctlSetInt(fd, unix.TUNSETIFF, int(uintptr(unsafe.Pointer(ifr))))
	if err != nil {
		return nil, fmt.Errorf("failed to set TUN device flags: %w", err)
	}

	// Retrieve the name that was generated based on the "tel%d" template.
	name := ifr.Name()

	// Set non-blocking so that ReadPacket() doesn't hang for several seconds when the
	// fd is Closed. ReadPacket() will still wait for data to arrive.
	//
	// See: https://github.com/golang/go/issues/30426#issuecomment-470044803
	err = unix.SetNonblock(fd, true)
	if err != nil {
		return nil, fmt.Errorf("failed to set TAP fd non-blocking: %w", err)
	}
	link, err := netlink.LinkByName(name)
	if err != nil {
		return nil, fmt.Errorf("failed to retrieve TAP link: %w", err)
	}
	err = netlink.LinkSetUp(link)
	if err != nil {
		return nil, fmt.Errorf("failed to set link UP: %w", err)
	}
	attrs := link.Attrs()
	d := &device{fd: fd, name: name, interfaceIndex: uint32(attrs.Index), isTAP: useTAP}
	if err = d.assignOwnedAddresses(ctx, link); err != nil {
		return nil, err
	}
	return d, nil
}

// assignOwnedAddresses gives the device a single source address per family from a
// Telepresence-owned range. The device routes cluster subnets as a router rather than
// claiming them as interface addresses, so it needs one address of its own to source
// locally-originated traffic toward the cluster and to receive the replies.
func (d *device) assignOwnedAddresses(ctx context.Context, link netlink.Link) error {
	for _, ipv4 := range []bool{true, false} {
		owned, ok := ownedAddress(ctx, ipv4, d.interfaceIndex)
		if !ok {
			continue
		}
		addr := &netlink.Addr{IPNet: subnet.PrefixToIPNet(netip.PrefixFrom(owned, owned.BitLen()))}
		if err := netlink.AddrAdd(link, addr); err != nil && !errors.Is(err, unix.EEXIST) {
			if ipv4 {
				return fmt.Errorf("failed to add owned address %s to interface %s: %w", owned, d.name, err)
			}
			// IPv6 may be disabled on the interface. The owned IPv6 address is only
			// needed when IPv6 subnets are routed, so a failure here is not fatal.
			clog.Warnf(ctx, "failed to add owned IPv6 address %s to interface %s: %v", owned, d.name, err)
		}
	}
	return nil
}

// addSubnet is a no-op on Linux. The device owns a single source address per family
// (assigned at bring-up); cluster subnets are routed by the Router through its policy
// table rather than claimed as interface addresses, so there is nothing to do per
// subnet here.
func (d *device) addSubnet(_ context.Context, _ netip.Prefix) error {
	return nil
}

// removeSubnet is a no-op on Linux; see addSubnet. The Router removes the subnet's
// route from its policy table.
func (d *device) removeSubnet(_ context.Context, _ netip.Prefix) error {
	return nil
}

func (d *device) getMTU() (mtu uint32, err error) {
	return rawfile.GetMTU(d.name)
}

func (d *device) createLinkEndpoint() (stack.LinkEndpoint, error) {
	mtu, err := d.getMTU()
	if err != nil {
		return nil, err
	}
	opts := &fdbased.Options{
		FDs:                []int{d.fd},
		MTU:                mtu,
		PacketDispatchMode: fdbased.RecvMMsg,
	}
	if d.isTAP {
		mac := RandomMAC()
		opts.EthernetHeader = true
		opts.Address = tcpip.LinkAddress(mac)
	}
	ep, err := fdbased.New(opts)
	if err != nil {
		return nil, err
	}
	d.endPoint = ep
	return ep, nil
}

func (d *device) Close() {
	if d.endPoint != nil {
		d.endPoint.Close()
	}
	if d.fd >= 0 {
		_ = unix.Close(d.fd)
	}
}

func (d *device) WaitForDevice() {
}
