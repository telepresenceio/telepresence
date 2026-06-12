package vif

import (
	"context"
	cryptoRand "crypto/rand"
	"encoding/binary"
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
	return &device{fd: fd, name: name, interfaceIndex: uint32(attrs.Index), isTAP: useTAP}, nil
}

// vifAddr returns the netlink address that assigns the given prefix to the device.
// A prefix that has an anchor is assigned as the peer prefix of the anchor address.
// The kernel then derives the same connected route for the prefix, but the device
// claims only the anchor in the local routing table.
func vifAddr(ctx context.Context, pfx netip.Prefix) *netlink.Addr {
	if anchor, ok := subnetAnchor(ctx, pfx); ok {
		return &netlink.Addr{
			IPNet: subnet.PrefixToIPNet(netip.PrefixFrom(anchor, anchor.BitLen())),
			Peer:  subnet.PrefixToIPNet(pfx),
			// Suppress the broadcast address that AddrAdd would otherwise derive
			// from the anchor and the peer prefix mask, which would claim an
			// address inside the virtual subnet. Without it, the kernel derives
			// the peer prefix's own broadcast entry, which addSubnet removes.
			Broadcast: net.IPv4zero,
		}
	}
	return &netlink.Addr{IPNet: subnet.PrefixToIPNet(pfx)}
}

func (d *device) addSubnet(ctx context.Context, pfx netip.Prefix) error {
	link, err := netlink.LinkByIndex(int(d.interfaceIndex))
	if err != nil {
		return fmt.Errorf("failed to find link for interface %s: %w", d.name, err)
	}
	if err := netlink.AddrAdd(link, vifAddr(ctx, pfx)); err != nil {
		return fmt.Errorf("failed to add address %s to interface %s: %w", pfx, d.name, err)
	}
	// The kernel derives a broadcast entry for the subnet's highest address from the
	// address assignment and refuses unicast connects to that address, but the address
	// is a perfectly valid pod IP. Link-level broadcast has no meaning on an L3 TUN
	// device, so the entry is removed.
	if bc, ok := subnetBroadcast(pfx); ok {
		err := netlink.RouteDel(&netlink.Route{
			LinkIndex: link.Attrs().Index,
			Table:     unix.RT_TABLE_LOCAL,
			Type:      unix.RTN_BROADCAST,
			Scope:     netlink.SCOPE_LINK,
			Dst:       subnet.PrefixToIPNet(netip.PrefixFrom(bc, bc.BitLen())),
		})
		if err != nil && !errors.Is(err, unix.ESRCH) {
			return fmt.Errorf("failed to remove broadcast entry %s from interface %s: %w", bc, d.name, err)
		}
	}
	return nil
}

// subnetBroadcast returns the broadcast address that the kernel derives when the given
// prefix is assigned to an interface. The second return value is false when no broadcast
// entry is created for the prefix.
func subnetBroadcast(pfx netip.Prefix) (netip.Addr, bool) {
	if !pfx.Addr().Is4() || pfx.Bits() >= 31 {
		return netip.Addr{}, false
	}
	bc := pfx.Masked().Addr().As4()
	v := binary.BigEndian.Uint32(bc[:]) | (uint32(1)<<(32-pfx.Bits()) - 1)
	binary.BigEndian.PutUint32(bc[:], v)
	return netip.AddrFrom4(bc), true
}

func (d *device) removeSubnet(ctx context.Context, pfx netip.Prefix) error {
	link, err := netlink.LinkByIndex(int(d.interfaceIndex))
	if err != nil {
		return err
	}
	return netlink.AddrDel(link, vifAddr(ctx, pfx))
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
