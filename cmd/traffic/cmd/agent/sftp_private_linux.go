package agent

import (
	"net"
	"os"

	"golang.org/x/sys/unix"
)

// socketPair returns two connected stream sockets that support half-close.
func socketPair() (a, b net.Conn, err error) {
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return nil, nil, err
	}
	if a, err = fdConn(fds[0]); err != nil {
		_ = unix.Close(fds[1])
		return nil, nil, err
	}
	if b, err = fdConn(fds[1]); err != nil {
		_ = a.Close()
		return nil, nil, err
	}
	return a, b, nil
}

func fdConn(fd int) (net.Conn, error) {
	f := os.NewFile(uintptr(fd), "sftp-socketpair")
	defer f.Close()
	return net.FileConn(f)
}
