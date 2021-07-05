package tun

import (
	"context"
	"net"
	"unsafe"

	"golang.org/x/sys/unix"

	"github.com/telepresenceio/telepresence/v2/pkg/tun/buffer"
)

// OpenTun creates a new TUN device and ensures that it is up and running.
func OpenTun(ctx context.Context) (*Device, error) {
	return openTun(ctx)
}

// AddSubnet adds a subnet to this TUN device and creates a route for that subnet which
// is associated with the device (removing the device will automatically remove the route).
func (t *Device) AddSubnet(ctx context.Context, subnet *net.IPNet) error {
	return t.addSubnet(ctx, subnet)
}

// RemoveSubnet removes a subnet from this TUN device and also removes the route for that subnet which
// is associated with the device.
func (t *Device) RemoveSubnet(ctx context.Context, subnet *net.IPNet) error {
	return t.removeSubnet(ctx, subnet)
}

// Name returns the name of this device, e.g. "tun0"
func (t *Device) Name() string {
	return t.name
}

// ReadPacket reads as many bytes as possible into the given buffer.Data and returns the
// number of bytes actually read
func (t *Device) ReadPacket(into *buffer.Data) (int, error) {
	return t.readPacket(into)
}

// WritePacket writes bytes from the given buffer.Data and returns the number of bytes
// actually written.
func (t *Device) WritePacket(from *buffer.Data) (int, error) {
	return t.writePacket(from)
}

func (t *Device) SetMTU(mtu int) error {
	return t.setMTU(mtu)
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
