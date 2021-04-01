package tun

import (
	"net"
	"unsafe"

	"golang.org/x/sys/unix"
)

// Name returns the name of this device, e.g. "tun0"
func (t *Device) Name() string {
	return t.name
}

func withSocket(domain int, f func(fd int) error) error {
	fd, err := unix.Socket(domain, unix.SOCK_DGRAM, 0)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	return f(fd)
}

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

func ioctl(socket int, request uint, requestData unsafe.Pointer) error {
	return unix.IoctlSetInt(socket, request, int(uintptr(requestData)))
}
