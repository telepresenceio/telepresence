package vif

import (
	"context"
	"fmt"
	"net"
	"os"
	"runtime"
	"unsafe"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"

	"github.com/telepresenceio/telepresence/v2/pkg/vif/buffer"
)

const devicePath = "/dev/net/tun"

type nativeDevice struct {
	*os.File
	name           string
	interfaceIndex int32
}

func openTun(_ context.Context) (*nativeDevice, error) {
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

	var flagsRequest struct {
		name  [unix.IFNAMSIZ]byte
		flags int16
	}
	copy(flagsRequest.name[:], "tel%d")
	flagsRequest.flags = unix.IFF_TUN | unix.IFF_NO_PI

	err = unix.IoctlSetInt(fd, unix.TUNSETIFF, int(uintptr(unsafe.Pointer(&flagsRequest))))
	if err != nil {
		return nil, fmt.Errorf("failed to set TUN device flags: %w", err)
	}

	// Retrieve the name that was generated based on the "tel%d" template. The
	// name is zero terminated.
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

	// Set non-blocking so that ReadPacket() doesn't hang for several seconds when the
	// fd is Closed. ReadPacket() will still wait for data to arrive.
	//
	// See: https://github.com/golang/go/issues/30426#issuecomment-470044803
	_ = unix.SetNonblock(fd, true)

	// Bring the device up. This is how it's done in ifconfig.
	provisioningSocket, err := unix.Socket(unix.AF_PACKET, unix.SOCK_DGRAM, unix.IPPROTO_IP)
	if err != nil {
		return nil, fmt.Errorf("failed to open provisioning socket: %w", err)
	}
	defer unix.Close(provisioningSocket)

	flagsRequest.flags = 0
	if err = ioctl(provisioningSocket, unix.SIOCGIFFLAGS, unsafe.Pointer(&flagsRequest)); err != nil {
		return nil, fmt.Errorf("failed to get flags for %s: %w", name, err)
	}

	flagsRequest.flags |= unix.IFF_UP | unix.IFF_RUNNING
	if err = ioctl(provisioningSocket, unix.SIOCSIFFLAGS, unsafe.Pointer(&flagsRequest)); err != nil {
		return nil, fmt.Errorf("failed to set flags for %s: %w", name, err)
	}

	index, err := getInterfaceIndex(provisioningSocket, name)
	if err != nil {
		return nil, err
	}
	return &nativeDevice{File: os.NewFile(uintptr(fd), devicePath), name: name, interfaceIndex: index}, nil
}

func (t *nativeDevice) Close() error {
	err := t.File.Close()
	if err != nil {
		return err
	}
	return nil
}

func (t *nativeDevice) addSubnet(ctx context.Context, subnet *net.IPNet) error {
	link, err := netlink.LinkByIndex(int(t.interfaceIndex))
	if err != nil {
		return fmt.Errorf("failed to find link for interface %s: %w", t.name, err)
	}
	addr := &netlink.Addr{IPNet: subnet}
	if err := netlink.AddrAdd(link, addr); err != nil {
		return fmt.Errorf("failed to add address %s to interface %s: %w", subnet, t.name, err)
	}
	return nil
}

func (t *nativeDevice) removeSubnet(ctx context.Context, subnet *net.IPNet) error {
	link, err := netlink.LinkByIndex(int(t.interfaceIndex))
	if err != nil {
		return err
	}
	addr := &netlink.Addr{IPNet: subnet}
	return netlink.AddrDel(link, addr)
}

func (t *nativeDevice) index() int32 {
	return t.interfaceIndex
}

func (t *nativeDevice) setMTU(mtu int) error {
	return withSocket(unix.AF_INET, func(fd int) error {
		var mtuRequest struct {
			name [unix.IFNAMSIZ]byte
			mtu  int32
		}
		copy(mtuRequest.name[:], t.name)
		mtuRequest.mtu = int32(mtu)
		err := ioctl(fd, unix.SIOCSIFMTU, unsafe.Pointer(&mtuRequest))
		runtime.KeepAlive(&mtuRequest)
		if err != nil {
			err = fmt.Errorf("set MTU on %s failed: %w", t.name, err)
		}
		return err
	})
}

func (t *nativeDevice) readPacket(into *buffer.Data) (int, error) {
	return t.File.Read(into.Raw())
}

func (t *nativeDevice) writePacket(from *buffer.Data, offset int) (int, error) {
	return t.File.Write(from.Raw()[offset:])
}

func getInterfaceIndex(fd int, name string) (int32, error) {
	var indexRequest struct {
		name  [unix.IFNAMSIZ]byte
		index int32
	}
	copy(indexRequest.name[:], name)
	if err := ioctl(fd, unix.SIOCGIFINDEX, unsafe.Pointer(&indexRequest)); err != nil {
		return 0, fmt.Errorf("get interface index on %s failed: %w", name, err)
	}
	return indexRequest.index, nil
}
