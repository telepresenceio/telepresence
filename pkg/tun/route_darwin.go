package tun

import (
	"net"
	"os"

	"golang.org/x/net/route"
	"golang.org/x/sys/unix"
)

// withRouteSocket will open the socket to where RouteMessages should be sent
// and call the given function with that socket. The socket is closed when the
// function returns
func withRouteSocket(f func(routeSocket int) error) error {
	routeSocket, err := unix.Socket(unix.AF_ROUTE, unix.SOCK_RAW, unix.AF_UNSPEC)
	if err != nil {
		return err
	}

	// Avoid the overhead of echoing messages back to sender
	if err = unix.SetsockoptInt(routeSocket, unix.SOL_SOCKET, unix.SO_USELOOPBACK, 0); err != nil {
		return err
	}
	defer unix.Close(routeSocket)
	return f(routeSocket)
}

// toRouteAddr converts an net.IP to its corresponding addrMessage.Addr
func toRouteAddr(ip net.IP) (addr route.Addr) {
	if ip4 := ip.To4(); ip4 != nil {
		dst := route.Inet4Addr{}
		copy(dst.IP[:], ip4)
		addr = &dst
	} else {
		dst := route.Inet6Addr{}
		copy(dst.IP[:], ip)
		addr = &dst
	}
	return addr
}

func toRouteMask(mask net.IPMask) (addr route.Addr) {
	if _, bits := mask.Size(); bits == 32 {
		dst := route.Inet4Addr{}
		copy(dst.IP[:], mask)
		addr = &dst
	} else {
		dst := route.Inet6Addr{}
		copy(dst.IP[:], mask)
		addr = &dst
	}
	return addr
}

func (t *Device) newRouteMessage(rtm, seq int, subnet *net.IPNet, gw net.IP) *route.RouteMessage {
	return &route.RouteMessage{
		Version: unix.RTM_VERSION,
		ID:      uintptr(os.Getpid()),
		Seq:     seq,
		Type:    rtm,
		Flags:   unix.RTF_UP | unix.RTF_STATIC | unix.RTF_CLONING,
		Addrs: []route.Addr{
			unix.RTAX_DST:     toRouteAddr(subnet.IP),
			unix.RTAX_GATEWAY: toRouteAddr(gw),
			unix.RTAX_NETMASK: toRouteMask(subnet.Mask),
		},
	}
}

func (t *Device) routeAdd(routeSocket, seq int, r *net.IPNet, gw net.IP) error {
	m := t.newRouteMessage(unix.RTM_ADD, seq, r, gw)
	wb, err := m.Marshal()
	if err != nil {
		return err
	}
	_, err = unix.Write(routeSocket, wb)
	if err == unix.EEXIST {
		// addrMessage exists, that's OK
		err = nil
	}
	return err
}

// nolint:unused
func (t *Device) routeClear(routeSocket, seq int, r *net.IPNet, gw net.IP) error {
	m := t.newRouteMessage(unix.RTM_DELETE, seq, r, gw)
	wb, err := m.Marshal()
	if err != nil {
		return err
	}
	_, err = unix.Write(routeSocket, wb)
	if err == unix.ESRCH {
		// addrMessage doesn't exist, that's OK
		err = nil
	}
	return err
}
