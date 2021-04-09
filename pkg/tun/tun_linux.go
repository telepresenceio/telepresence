// tun_linux.go: Open a Tunnel (L3 virtual interface) using the Universal TUN/TAP device driver.
package tun

import (
	"context"
	"fmt"
	"net"
	"os"
	"unsafe"

	"golang.org/x/sys/unix"

	"github.com/datawire/dlib/dexec"
	"github.com/telepresenceio/telepresence/v2/pkg/tun/buffer"
)

const devicePath = "/dev/net/tun"

type Device struct {
	*os.File
	name  string
	index uint32
}

// OpenTun creates a new TUN device and ensures that it is up and running.
func OpenTun() (*Device, error) {
	// https://www.kernel.org/doc/Documentation/networking/tuntap.txt

	fd, err := unix.Open(devicePath, unix.O_RDWR, 0)
	if err != nil {
		return nil, err
	}

	var flagsRequest struct {
		name  [unix.IFNAMSIZ]byte
		flags int16
	}
	copy(flagsRequest.name[:], "tel%d")
	flagsRequest.flags = unix.IFF_TUN | unix.IFF_NO_PI

	err = unix.IoctlSetInt(fd, unix.TUNSETIFF, int(uintptr(unsafe.Pointer(&flagsRequest))))
	if err != nil {
		_ = unix.Close(fd)
		return nil, err
	}

	var name string
	for i := 0; i < unix.IFNAMSIZ; i++ {
		if flagsRequest.name[i] == 0 {
			name = string(flagsRequest.name[0:i])
			break
		}
	}
	if name == "" {
		name = string(flagsRequest.name[:])
	}

	htons := func(value uint16) uint16 {
		test := uint16(1)
		if (*[2]byte)(unsafe.Pointer(&test))[0] == 1 {
			// this machine is little endian, swap bytes
			value = value&0xff<<8 | value&0xff00>>8
		}
		return value
	}

	// Passing a network ordered short here is required.
	provisioningSocket, err := unix.Socket(unix.AF_PACKET, unix.SOCK_RAW, int(htons(unix.ETH_P_ALL)))
	if err != nil {
		return nil, err
	}
	if err = unix.BindToDevice(provisioningSocket, name); err != nil {
		return nil, err
	}
	flagsRequest.flags |= unix.IFF_UP | unix.IFF_RUNNING
	if err = ioctl(provisioningSocket, unix.SIOCSIFFLAGS, unsafe.Pointer(&flagsRequest)); err != nil {
		return nil, err
	}

	index, err := getInterfaceIndex(provisioningSocket, name)
	if err != nil {
		return nil, err
	}

	// Set non-blocking so that Read() doesn't hang for several seconds when the
	// fd is Closed. Read() will still wait for data to arrive.
	//
	// See: https://github.com/golang/go/issues/30426#issuecomment-470044803
	_ = unix.SetNonblock(fd, true)
	return &Device{File: os.NewFile(uintptr(fd), devicePath), name: name, index: index}, nil
}

func (t *Device) AddSubnet(ctx context.Context, subnet *net.IPNet) error {
	return dexec.CommandContext(ctx, "ip", "a", "add", subnet.String(), "dev", t.name).Run()
}

// RemoveSubnet removes a subnet from this TUN device and also removes the route for that subnet which
// is associated with the device.
func (t *Device) RemoveSubnet(ctx context.Context, subnet *net.IPNet) error {
	return dexec.CommandContext(ctx, "ip", "a", "del", subnet.String(), "dev", t.name).Run()
}

// Index returns the index of this device
func (t *Device) Index() uint32 {
	return t.index
}

func (t *Device) SetMTU(mtu int) error {
	return withSocket(unix.AF_INET, func(fd int) error {
		var mtuRequest struct {
			name [unix.IFNAMSIZ]byte
			mtu  uint32
		}
		copy(mtuRequest.name[:], t.name)
		mtuRequest.mtu = uint32(mtu)
		err := ioctl(fd, unix.SIOCSIFMTU, unsafe.Pointer(&mtuRequest))
		if err != nil {
			err = fmt.Errorf("set MTU on %s failed: %w", t.name, err)
		}
		return err
	})
}

// Read reads as many bytes as possible into the given buffer.Data and returns the
// number of bytes actually read
func (t *Device) Read(into *buffer.Data) (int, error) {
	return t.File.Read(into.Raw())
}

// Write writes bytes from the given buffer.Data and returns the number of bytes
// actually written.
func (t *Device) Write(from *buffer.Data) (int, error) {
	return t.File.Write(from.Raw())
}

func getInterfaceIndex(fd int, name string) (uint32, error) {
	var indexRequest struct {
		name  [unix.IFNAMSIZ]byte
		index uint32
	}
	copy(indexRequest.name[:], name)
	if err := ioctl(fd, unix.SIOCGIFINDEX, unsafe.Pointer(&indexRequest)); err != nil {
		return 0, fmt.Errorf("get interface index on %s failed: %w", name, err)
	}
	return indexRequest.index, nil
}
