package tun

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

	"github.com/telepresenceio/telepresence/v2/pkg/tun/buffer"
)

const sysProtoControl = 2
const uTunOptIfName = 2
const uTunControlName = "com.apple.net.utun_control"

type Device struct {
	*os.File
	name string
}

func openTun(_ context.Context) (*Device, error) {
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
	return &Device{
		File: os.NewFile(uintptr(fd), ""),
		name: name,
	}, nil
}

func (t *Device) addSubnet(_ context.Context, subnet *net.IPNet) error {
	to := make(net.IP, len(subnet.IP))
	copy(to, subnet.IP)
	to[len(to)-1] = 1
	if err := t.setAddr(subnet, to); err != nil {
		return err
	}
	return withRouteSocket(func(s int) error {
		return t.routeAdd(s, 1, subnet, to)
	})
}

func (t *Device) removeSubnet(_ context.Context, subnet *net.IPNet) error {
	to := make(net.IP, len(subnet.IP))
	copy(to, subnet.IP)
	to[len(to)-1] = 1
	if err := t.removeAddr(subnet, to); err != nil {
		return err
	}
	return withRouteSocket(func(s int) error {
		return t.routeClear(s, 1, subnet, to)
	})
}

func (t *Device) setMTU(mtu int) error {
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

func (t *Device) readPacket(into *buffer.Data) (int, error) {
	n, err := t.File.Read(into.Raw())
	if n >= buffer.PrefixLen {
		n -= buffer.PrefixLen
	}
	return n, err
}

func (t *Device) writePacket(from *buffer.Data) (int, error) {
	raw := from.Raw()
	if len(raw) <= buffer.PrefixLen {
		return 0, unix.EIO
	}

	ipVer := raw[buffer.PrefixLen] >> 4
	switch ipVer {
	case ipv4.Version:
		raw[3] = unix.AF_INET
	case ipv6.Version:
		raw[3] = unix.AF_INET6
	default:
		return 0, errors.New("unable to determine IP version from packet")
	}
	n, err := t.File.Write(raw)
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
type addrIfReq6 struct {
	name           [unix.IFNAMSIZ]byte
	addr           unix.RawSockaddrInet6
	dest           unix.RawSockaddrInet6
	mask           unix.RawSockaddrInet6
	flags          int32 //nolint:structcheck
	expire         int64 //nolint:structcheck
	preferred      int64 //nolint:structcheck
	validLifeTime  uint32
	prefixLifeTime uint32
}

// SIOCAIFADDR_IN6 is the same ioctlHandle identifier as unix.SIOCAIFADDR adjusted with size of addrIfReq6
const SIOCAIFADDR_IN6 = (unix.SIOCAIFADDR & 0xe000ffff) | (uint(unsafe.Sizeof(addrIfReq6{})) << 16)
const ND6_INFINITE_LIFETIME = 0xffffffff

// SIOCDIFADDR_IN6 is the same ioctlHandle identifier as unix.SIOCDIFADDR adjusted with size of addrIfReq6
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

func (t *Device) setAddr(subnet *net.IPNet, to net.IP) error {
	if sub4, to4, ok := addrToIp4(subnet, to); ok {
		return withSocket(unix.AF_INET, func(fd int) error {
			ifreq := &addrIfReq{
				addr: unix.RawSockaddrInet4{Len: 16, Family: unix.AF_INET},
				dest: unix.RawSockaddrInet4{Len: 16, Family: unix.AF_INET},
				mask: unix.RawSockaddrInet4{Len: 16, Family: unix.AF_INET},
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
				addr:           unix.RawSockaddrInet6{Len: 28, Family: unix.AF_INET6},
				dest:           unix.RawSockaddrInet6{Len: 28, Family: unix.AF_INET6},
				mask:           unix.RawSockaddrInet6{Len: 28, Family: unix.AF_INET6},
				validLifeTime:  ND6_INFINITE_LIFETIME,
				prefixLifeTime: ND6_INFINITE_LIFETIME,
			}
			copy(ifreq.name[:], t.name)
			copy(ifreq.addr.Addr[:], subnet.IP.To16())
			copy(ifreq.mask.Addr[:], subnet.Mask)
			copy(ifreq.dest.Addr[:], to.To16())
			err := ioctl(fd, SIOCAIFADDR_IN6, unsafe.Pointer(ifreq))
			runtime.KeepAlive(ifreq)
			return err
		})
	}
}

func (t *Device) removeAddr(subnet *net.IPNet, to net.IP) error {
	if sub4, to4, ok := addrToIp4(subnet, to); ok {
		return withSocket(unix.AF_INET, func(fd int) error {
			ifreq := &addrIfReq{
				addr: unix.RawSockaddrInet4{Len: 16, Family: unix.AF_INET},
				dest: unix.RawSockaddrInet4{Len: 16, Family: unix.AF_INET},
				mask: unix.RawSockaddrInet4{Len: 16, Family: unix.AF_INET},
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
				addr:           unix.RawSockaddrInet6{Len: 28, Family: unix.AF_INET6},
				dest:           unix.RawSockaddrInet6{Len: 28, Family: unix.AF_INET6},
				mask:           unix.RawSockaddrInet6{Len: 28, Family: unix.AF_INET6},
				validLifeTime:  ND6_INFINITE_LIFETIME,
				prefixLifeTime: ND6_INFINITE_LIFETIME,
			}
			copy(ifreq.name[:], t.name)
			copy(ifreq.addr.Addr[:], subnet.IP.To16())
			copy(ifreq.mask.Addr[:], subnet.Mask)
			copy(ifreq.dest.Addr[:], to.To16())
			err := ioctl(fd, SIOCDIFADDR_IN6, unsafe.Pointer(ifreq))
			runtime.KeepAlive(ifreq)
			return err
		})
	}
}
