package vif

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"runtime"
	"unsafe"

	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"
	"golang.org/x/sys/unix"

	"github.com/telepresenceio/telepresence/v2/pkg/routing"
	"github.com/telepresenceio/telepresence/v2/pkg/vif/buffer"
)

const (
	sysProtoControl = 2
	uTunOptIfName   = 2
	uTunControlName = "com.apple.net.utun_control"
)

type nativeDevice struct {
	*os.File
	name string
}

func openTun(_ context.Context) (*nativeDevice, error) {
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
	return &nativeDevice{
		File: os.NewFile(uintptr(fd), ""),
		name: name,
	}, nil
}

func (t *nativeDevice) addSubnet(_ context.Context, subnet *net.IPNet) error {
	to := make(net.IP, len(subnet.IP))
	copy(to, subnet.IP)
	to[len(to)-1] = 1
	if err := t.setAddr(subnet, to); err != nil {
		return err
	}
	return routing.Add(1, subnet, to)
}

func (t *nativeDevice) index() int32 {
	panic("not implemented")
}

func (t *nativeDevice) removeSubnet(_ context.Context, subnet *net.IPNet) error {
	to := make(net.IP, len(subnet.IP))
	copy(to, subnet.IP)
	to[len(to)-1] = 1
	if err := t.removeAddr(subnet, to); err != nil {
		return err
	}
	return routing.Clear(1, subnet, to)
}

func (t *nativeDevice) setMTU(mtu int) error {
	return withSocket(unix.AF_INET, func(fd int) error {
		var ifr unix.IfreqMTU
		copy(ifr.Name[:], t.name)
		ifr.MTU = int32(mtu)
		err := unix.IoctlSetIfreqMTU(fd, &ifr)
		if err != nil {
			err = fmt.Errorf("set MTU on %s failed: %w", t.name, err)
		}
		return err
	})
}

func (t *nativeDevice) readPacket(into *buffer.Data) (int, error) {
	n, err := t.File.Read(into.Raw())
	if n >= buffer.PrefixLen {
		n -= buffer.PrefixLen
	}
	return n, err
}

func (t *nativeDevice) writePacket(from *buffer.Data, offset int) (n int, err error) {
	raw := from.Raw()
	if len(raw) <= buffer.PrefixLen {
		return 0, unix.EIO
	}

	ipVer := raw[buffer.PrefixLen] >> 4
	var af byte
	switch ipVer {
	case ipv4.Version:
		af = unix.AF_INET
	case ipv6.Version:
		af = unix.AF_INET6
	default:
		return 0, errors.New("unable to determine IP version from packet")
	}

	if offset > 0 {
		raw = raw[offset:]
		// Temporarily move AF_INET/AF_INET6 into the offset position.
		r3 := raw[3]
		raw[3] = af
		n, err = t.File.Write(raw)
		raw[3] = r3
	} else {
		raw[3] = af
		n, err = t.File.Write(raw)
	}
	return n - buffer.PrefixLen, err
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

// SIOCDIFADDR_IN6 is the same ioctlHandle identifier as unix.SIOCDIFADDR adjusted with size of addrIfReq6.
const SIOCDIFADDR_IN6 = (unix.SIOCDIFADDR & 0xe000ffff) | (uint(unsafe.Sizeof(addrIfReq6{})) << 16)

func addrToIp4(subnet *net.IPNet, to net.IP) (*net.IPNet, net.IP, bool) {
	if to4 := to.To4(); to4 != nil {
		if dest4 := subnet.IP.To4(); dest4 != nil {
			if _, bits := subnet.Mask.Size(); bits == 32 {
				return &net.IPNet{IP: dest4, Mask: subnet.Mask}, to4, true
			}
		}
	}
	return nil, nil, false
}

func (t *nativeDevice) setAddr(subnet *net.IPNet, to net.IP) error {
	if sub4, to4, ok := addrToIp4(subnet, to); ok {
		return withSocket(unix.AF_INET, func(fd int) error {
			ifreq := &addrIfReq{
				addr: unix.RawSockaddrInet4{Len: unix.SizeofSockaddrInet4, Family: unix.AF_INET},
				dest: unix.RawSockaddrInet4{Len: unix.SizeofSockaddrInet4, Family: unix.AF_INET},
				mask: unix.RawSockaddrInet4{Len: unix.SizeofSockaddrInet4, Family: unix.AF_INET},
			}
			copy(ifreq.name[:], t.name)
			copy(ifreq.addr.Addr[:], sub4.IP)
			copy(ifreq.mask.Addr[:], sub4.Mask)
			copy(ifreq.dest.Addr[:], to4)
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

			copy(ifreq.name[:], t.name)
			copy(ifreq.addr.Addr[:], subnet.IP.To16())
			copy(ifreq.mask.Addr[:], subnet.Mask)
			err := ioctl(fd, SIOCAIFADDR_IN6, unsafe.Pointer(ifreq))
			runtime.KeepAlive(ifreq)
			return err
		})
	}
}

func (t *nativeDevice) removeAddr(subnet *net.IPNet, to net.IP) error {
	if sub4, to4, ok := addrToIp4(subnet, to); ok {
		return withSocket(unix.AF_INET, func(fd int) error {
			ifreq := &addrIfReq{
				addr: unix.RawSockaddrInet4{Len: unix.SizeofSockaddrInet6, Family: unix.AF_INET},
				dest: unix.RawSockaddrInet4{Len: unix.SizeofSockaddrInet6, Family: unix.AF_INET},
				mask: unix.RawSockaddrInet4{Len: unix.SizeofSockaddrInet6, Family: unix.AF_INET},
			}
			copy(ifreq.name[:], t.name)
			copy(ifreq.addr.Addr[:], sub4.IP)
			copy(ifreq.mask.Addr[:], sub4.Mask)
			copy(ifreq.dest.Addr[:], to4)
			err := ioctl(fd, unix.SIOCDIFADDR, unsafe.Pointer(ifreq))
			runtime.KeepAlive(ifreq)
			return err
		})
	} else {
		return withSocket(unix.AF_INET6, func(fd int) error {
			ifreq := &addrIfReq6{
				addr: unix.RawSockaddrInet6{Len: 28, Family: unix.AF_INET6},
				dest: unix.RawSockaddrInet6{Len: 28, Family: unix.AF_INET6},
				mask: unix.RawSockaddrInet6{Len: 28, Family: unix.AF_INET6},
			}
			ifreq.addrLifetime.validLifeTime = ND6_INFINITE_LIFETIME
			ifreq.addrLifetime.prefixLifeTime = ND6_INFINITE_LIFETIME

			copy(ifreq.name[:], t.name)
			copy(ifreq.addr.Addr[:], subnet.IP.To16())
			copy(ifreq.mask.Addr[:], subnet.Mask)
			err := ioctl(fd, SIOCDIFADDR_IN6, unsafe.Pointer(ifreq))
			runtime.KeepAlive(ifreq)
			return err
		})
	}
}
