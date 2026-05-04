package vif

import (
	"context"
	cryptoRand "crypto/rand"
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

func (d *device) addSubnet(_ context.Context, pfx netip.Prefix) error {
	link, err := netlink.LinkByIndex(int(d.interfaceIndex))
	if err != nil {
		return fmt.Errorf("failed to find link for interface %s: %w", d.name, err)
	}
	addr := &netlink.Addr{IPNet: subnet.PrefixToIPNet(pfx)}
	if err := netlink.AddrAdd(link, addr); err != nil {
		return fmt.Errorf("failed to add address %s to interface %s: %w", pfx, d.name, err)
	}
	return nil
}

func (d *device) removeSubnet(_ context.Context, pfx netip.Prefix) error {
	link, err := netlink.LinkByIndex(int(d.interfaceIndex))
	if err != nil {
		return err
	}
	addr := &netlink.Addr{IPNet: subnet.PrefixToIPNet(pfx)}
	return netlink.AddrDel(link, addr)
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
