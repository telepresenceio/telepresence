// tun_linux.go: Open a Tunnel (L3 virtual interface) using the Universal TUN/TAP device driver.
package tun

import (
	"io"
	"os"

	"golang.org/x/sys/unix"
)

func OpenTun() (io.ReadWriteCloser, string, error) {
	// https://www.kernel.org/doc/Documentation/networking/tuntap.txt

	fd, err := unix.Open("/dev/net/tun", unix.O_RDWR, 0)
	if err != nil {
		return nil, "", err
	}

	name, err := IoctlTunSetInterfaceFlags(fd, "tel%d", unix.IFF_TUN|unix.IFF_NO_PI)
	if err != nil {
		_ = unix.Close(fd)
		return nil, "", err
	}

	// Set non-blocking so that Read() doesn't hang for several seconds when the
	// fd is Closed. Read() will still wait for data to arrive.
	//
	// See: https://github.com/golang/go/issues/30426#issuecomment-470044803
	_ = unix.SetNonblock(fd, true)
	wrapper := os.NewFile(uintptr(fd), name)
	return wrapper, name, nil
}
