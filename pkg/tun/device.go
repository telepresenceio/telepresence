package tun

import (
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

func ioctl(socket int, request uint, requestData unsafe.Pointer) error {
	return unix.IoctlSetInt(socket, request, int(uintptr(requestData)))
}
