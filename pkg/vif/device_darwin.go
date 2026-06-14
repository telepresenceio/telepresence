package vif

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"runtime"
	"sync"
	"unsafe"

	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"
	"golang.org/x/sys/unix"
	"gvisor.dev/gvisor/pkg/tcpip/link/channel"
	"gvisor.dev/gvisor/pkg/tcpip/stack"

	"github.com/telepresenceio/clog"
	"github.com/telepresenceio/telepresence/v2/pkg/routing"
)

const (
	sysProtoControl = 2
	uTunOptIfName   = 2
	uTunControlName = "com.apple.net.utun_control"
)

type device struct {
	*channel.Endpoint
	file           *os.File
	ctx            context.Context
	name           string
	interfaceIndex uint32
	wb             bytes.Buffer
	wg             sync.WaitGroup
}

func openTun(ctx context.Context) (*device, error) {
	fd, err := unix.Socket(unix.AF_SYSTEM, unix.SOCK_DGRAM, sysProtoControl)
	if err != nil {
		return nil, fmt.Errorf("failed to open DGRAM socket: %w", err)
	}
	unix.CloseOnExec(fd)
	defer func() {
		if err != nil {
			_ = unix.Close(fd)
		}
	}()

	info := &unix.CtlInfo{}
	copy(info.Name[:], uTunControlName)
	if err = unix.IoctlCtlInfo(fd, info); err != nil {
		return nil, fmt.Errorf("failed to getBuffer IOCTL info for %s: %w", uTunControlName, err)
	}

	if err = unix.Connect(fd, &unix.SockaddrCtl{ID: info.Id, Unit: 0}); err != nil {
		return nil, err
	}

	if err = unix.SetNonblock(fd, true); err != nil {
		return nil, err
	}

	name, err := unix.GetsockoptString(fd, sysProtoControl, uTunOptIfName)
	if err != nil {
		return nil, err
	}
	iface, err := net.InterfaceByName(name)
	if err != nil {
		return nil, err
	}
	d := &device{
		file:           os.NewFile(uintptr(fd), ""),
		ctx:            ctx,
		name:           name,
		interfaceIndex: uint32(iface.Index),
		Endpoint:       channel.New(defaultDevOutQueueLen, uint32(iface.MTU), ""),
	}
	if err = d.assignOwnedAddresses(ctx); err != nil {
		return nil, err
	}
	return d, nil
}

// Close closes both the tun-device and the Endpoint. This function overrides the LinkEndpoint.Close so
// it can not return an error.
func (d *device) Close() {
	d.Endpoint.Close()
	_ = d.file.Close()
}

// assignOwnedAddresses gives the device a single source address per family from a
// Telepresence-owned range. macOS is strong-host, so the address must be unique to this
// device — ownedAddress guarantees that. The device routes cluster subnets as a router
// rather than claiming them as interface addresses, so it needs one address of its own to
// source locally-originated traffic and to receive the replies. The address is assigned as
// a point-to-point pair to itself (a /32 or /128), which a utun interface requires.
func (d *device) assignOwnedAddresses(ctx context.Context) error {
	for _, ipv4 := range []bool{true, false} {
		owned, ok := ownedAddress(ctx, ipv4, d.interfaceIndex)
		if !ok {
			continue
		}
		if err := d.setAddr(netip.PrefixFrom(owned, owned.BitLen()), owned); err != nil {
			if ipv4 {
				return fmt.Errorf("failed to add owned address %s to %s: %w", owned, d.name, err)
			}
			// IPv6 may be disabled on the interface; the owned IPv6 address is only needed
			// when IPv6 subnets are routed, so a failure here is not fatal.
			clog.Warnf(ctx, "failed to add owned IPv6 address %s to %s: %v", owned, d.name, err)
		}
	}
	return nil
}

// addSubnet routes a cluster subnet through the device interface. The device owns a single
// source address (assigned at bring-up), so the subnet is added as an interface route
// rather than the device claiming it as a point-to-point address. macOS then selects the
// owned address as the source for traffic to the subnet.
func (d *device) addSubnet(_ context.Context, subnet netip.Prefix) error {
	return routing.AddViaInterface(1, subnet, int(d.interfaceIndex))
}

func (d *device) removeSubnet(_ context.Context, subnet netip.Prefix) error {
	return routing.ClearViaInterface(1, subnet, int(d.interfaceIndex))
}

func (d *device) readPacket(buf []byte) (int, error) {
	return d.file.Read(buf)
}

const prefixLen = 4

func (d *device) headerSkip() int {
	return prefixLen
}

func (d *device) writePacket(from *stack.PacketBuffer) (err error) {
	ss := from.AsSlices()
	var first []byte
	for _, s := range ss {
		if len(s) > 0 {
			first = s
			break
		}
	}
	if first == nil {
		return nil
	}
	ipVer := first[0] >> 4
	var af byte
	switch ipVer {
	case ipv4.Version:
		af = unix.AF_INET
	case ipv6.Version:
		af = unix.AF_INET6
	default:
		return errors.New("unable to determine IP version from packet")
	}
	wb := &d.wb
	wb.Reset()
	wb.WriteByte(0)
	wb.WriteByte(0)
	wb.WriteByte(0)
	wb.WriteByte(af)
	wb.Write(first)
	for i := 1; i < len(ss); i++ {
		wb.Write(ss[i])
	}
	_, err = d.file.Write(wb.Bytes())
	return err
}

// Address structure for the SIOCAIFADDR ioctlHandle request
//
// See https://www.unix.com/man-page/osx/4/netintro/
type addrIfReq struct {
	name [unix.IFNAMSIZ]byte
	addr unix.RawSockaddrInet4
	dest unix.RawSockaddrInet4
	mask unix.RawSockaddrInet4
}

// Address structure for the SIOCAIFADDR_IN6 ioctlHandle request
//
// See https://www.unix.com/man-page/osx/4/netintro/

type addrLifetime struct {
	expire         float64 //nolint:unused //not used
	preferred      float64 //nolint:unused // not used
	validLifeTime  uint32
	prefixLifeTime uint32
}

type addrIfReq6 struct {
	name         [unix.IFNAMSIZ]byte
	addr         unix.RawSockaddrInet6
	dest         unix.RawSockaddrInet6
	mask         unix.RawSockaddrInet6
	flags        int32 //nolint:structcheck // this is the type returned by the kernel, not our own type
	addrLifetime addrLifetime
}

// SIOCAIFADDR_IN6 is the same ioctlHandle identifier as unix.SIOCAIFADDR adjusted with size of addrIfReq6.
const (
	SIOCAIFADDR_IN6       = (unix.SIOCAIFADDR & 0xe000ffff) | (uint(unsafe.Sizeof(addrIfReq6{})) << 16)
	ND6_INFINITE_LIFETIME = 0xffffffff
	IN6_IFF_NODAD         = 0x0020
	IN6_IFF_SECURED       = 0x0400
)

func (d *device) setAddr(subnet netip.Prefix, to netip.Addr) error {
	if to.Is4() && subnet.Addr().Is4() {
		return withSocket(unix.AF_INET, func(fd int) error {
			ifreq := &addrIfReq{
				addr: unix.RawSockaddrInet4{Len: unix.SizeofSockaddrInet4, Family: unix.AF_INET},
				dest: unix.RawSockaddrInet4{Len: unix.SizeofSockaddrInet4, Family: unix.AF_INET},
				mask: unix.RawSockaddrInet4{Len: unix.SizeofSockaddrInet4, Family: unix.AF_INET},
			}
			copy(ifreq.name[:], d.name)
			copy(ifreq.mask.Addr[:], net.CIDRMask(subnet.Bits(), 32))
			ifreq.addr.Addr = subnet.Addr().As4()
			ifreq.dest.Addr = to.As4()
			err := ioctl(fd, unix.SIOCAIFADDR, unsafe.Pointer(ifreq))
			runtime.KeepAlive(ifreq)
			return err
		})
	} else {
		return withSocket(unix.AF_INET6, func(fd int) error {
			ifreq := &addrIfReq6{
				addr:  unix.RawSockaddrInet6{Len: unix.SizeofSockaddrInet6, Family: unix.AF_INET6},
				mask:  unix.RawSockaddrInet6{Len: unix.SizeofSockaddrInet6, Family: unix.AF_INET6},
				flags: IN6_IFF_NODAD | IN6_IFF_SECURED,
			}
			ifreq.addrLifetime.validLifeTime = ND6_INFINITE_LIFETIME
			ifreq.addrLifetime.prefixLifeTime = ND6_INFINITE_LIFETIME

			copy(ifreq.name[:], d.name)
			copy(ifreq.mask.Addr[:], net.CIDRMask(subnet.Bits(), 128))
			ifreq.addr.Addr = subnet.Addr().As16()
			err := ioctl(fd, SIOCAIFADDR_IN6, unsafe.Pointer(ifreq))
			runtime.KeepAlive(ifreq)
			return err
		})
	}
}

func withSocket(domain int, f func(fd int) error) error {
	fd, err := unix.Socket(domain, unix.SOCK_DGRAM, 0)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	return f(fd)
}

func ioctl(socket int, request uint, requestData unsafe.Pointer) error {
	return unix.IoctlSetInt(socket, request, int(uintptr(requestData)))
}
