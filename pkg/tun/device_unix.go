// +build !windows

package tun

import (
	"context"
	"net"
	"unsafe"

	"golang.org/x/sys/unix"
)

func (t *Device) setDNS(_ context.Context, server net.IP, domains []string) (err error) {
	// DNS is configured by other means than through the actual device
	return nil
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
