package nat

import (
	"context"
	"fmt"
	"net"
	"os/exec"
	"strings"
	"syscall"

	"github.com/datawire/dlib/dlog"
	"github.com/datawire/telepresence2/v2/pkg/client/logging"
)

type iptablesRouter struct {
	routingTableCommon
}

func newRouter(name string) FirewallRouter {
	return &iptablesRouter{
		routingTableCommon: routingTableCommon{
			Name:     name,
			Mappings: make(map[Address]string),
		},
	}
}

func (t *iptablesRouter) ipt(c context.Context, args ...string) error {
	// Deliberately avoiding dexec here as this cannot be interrupted when cleaning up
	dlog.Debugf(c, "running %s", logging.ShellString("iptables", args))
	return exec.Command("iptables", append([]string{"-t", "nat"}, args...)...).Run()
}

func (t *iptablesRouter) Enable(c context.Context) (err error) {
	// XXX: -D only removes one copy of the rule, need to figure out how to remove all copies just in case
	_ = t.ipt(c, "-D", "OUTPUT", "-j", t.Name)
	// we need to be in the PREROUTING chain in order to get traffic
	// from docker containers, not sure you would *always* want this,
	// but probably makes sense as a default
	_ = t.ipt(c, "-D", "PREROUTING", "-j", t.Name)

	// ensure that the chain exists
	_ = t.ipt(c, "-N", t.Name)

	// Flush the chain
	if err = t.ipt(c, "-F", t.Name); err != nil {
		return err
	}

	if err = t.ipt(c, "-I", "OUTPUT", "1", "-j", t.Name); err != nil {
		return err
	}
	if err = t.ipt(c, "-I", "PREROUTING", "1", "-j", t.Name); err != nil {
		return err
	}
	return t.ipt(c, "-A", t.Name, "-j", "RETURN", "--dest", "127.0.0.1/32", "-p", "tcp")
}

func (t *iptablesRouter) Disable(c context.Context) (err error) {
	// XXX: -D only removes one copy of the rule, need to figure out how to remove all copies just in case
	if err = t.ipt(c, "-D", "OUTPUT", "-j", t.Name); err != nil {
		return err
	}
	if err = t.ipt(c, "-D", "PREROUTING", "-j", t.Name); err != nil {
		return err
	}
	if err = t.ipt(c, "-F", t.Name); err != nil {
		return err
	}
	return t.ipt(c, "-X", t.Name)
}

func (t *iptablesRouter) ForwardTCP(c context.Context, ip, port, toPort string) error {
	return t.forward(c, "tcp", ip, port, toPort)
}

func (t *iptablesRouter) ForwardUDP(c context.Context, ip, port, toPort string) error {
	return t.forward(c, "udp", ip, port, toPort)
}

func (t *iptablesRouter) forward(c context.Context, protocol, ip, port, toPort string) error {
	_ = t.clear(c, protocol, ip, port)
	args := []string{"-A", t.Name, "-j", "REDIRECT", "-p", protocol, "--dest", ip + "/32"}
	if port != "" {
		if strings.Contains(port, ",") {
			args = append(args, "-m", "multiport", "--dports", port)
		} else {
			args = append(args, "--dport", port)
		}
	}
	args = append(args, "--to-ports", toPort)
	if err := t.ipt(c, args...); err != nil {
		return err
	}
	t.Mappings[Address{protocol, ip, port}] = toPort
	return nil
}

func (t *iptablesRouter) ClearTCP(c context.Context, ip, port string) error {
	return t.clear(c, "tcp", ip, port)
}

func (t *iptablesRouter) ClearUDP(c context.Context, ip, port string) error {
	return t.clear(c, "udp", ip, port)
}

func (t *iptablesRouter) clear(c context.Context, protocol, ip, port string) error {
	if previous, exists := t.Mappings[Address{protocol, ip, port}]; exists {
		args := []string{"-D", t.Name, "-j", "REDIRECT", "-p", protocol, "--dest", ip + "/32"}
		if port != "" {
			if strings.Contains(port, ",") {
				args = append(args, "-m", "multiport", "--dports", port)
			} else {
				args = append(args, "--dport", port)
			}
		}
		args = append(args, "--to-ports", previous)
		if err := t.ipt(c, args...); err != nil {
			return err
		}
		delete(t.Mappings, Address{protocol, ip, port})
	}
	return nil
}

const (
	SO_ORIGINAL_DST      = 80
	IP6T_SO_ORIGINAL_DST = 80
)

// GetOriginalDst gets the original destination for the socket when redirect by linux iptables
// refer to https://raw.githubusercontent.com/missdeer/avege/master/src/inbound/redir/redir_iptables.go
//
func (t *iptablesRouter) GetOriginalDst(conn *net.TCPConn) (rawaddr []byte, host string, err error) {
	var addr *syscall.IPv6Mreq

	// Get original destination
	// this is the only syscall in the Golang libs that I can find that returns 16 bytes
	// Example result: &{Multiaddr:[2 0 31 144 206 190 36 45 0 0 0 0 0 0 0 0] Interface:0}
	// port starts at the 3rd byte and is 2 bytes long (31 144 = port 8080)
	// IPv6 version, didn't find a way to detect network family
	// addr, err := syscall.GetsockoptIPv6Mreq(int(fd), syscall.IPPROTO_IPV6, IP6T_SO_ORIGINAL_DST)
	// IPv4 address starts at the 5th byte, 4 bytes long (206 190 36 45)
	rawConn, err := conn.SyscallConn()
	if err != nil {
		return nil, "", err
	}

	err = rawConn.Control(func(fd uintptr) {
		addr, err = syscall.GetsockoptIPv6Mreq(int(fd), syscall.IPPROTO_IP, SO_ORIGINAL_DST)
	})
	if err != nil {
		return nil, "", err
	}

	// \attention: IPv4 only!!!
	// address type, 1 - IPv4, 4 - IPv6, 3 - hostname, only IPv4 is supported now
	rawaddr = append(rawaddr, byte(1))
	// raw IP address, 4 bytes for IPv4 or 16 bytes for IPv6, only IPv4 is supported now
	rawaddr = append(rawaddr, addr.Multiaddr[4])
	rawaddr = append(rawaddr, addr.Multiaddr[5])
	rawaddr = append(rawaddr, addr.Multiaddr[6])
	rawaddr = append(rawaddr, addr.Multiaddr[7])
	// port
	rawaddr = append(rawaddr, addr.Multiaddr[2])
	rawaddr = append(rawaddr, addr.Multiaddr[3])

	host = fmt.Sprintf("%d.%d.%d.%d:%d",
		addr.Multiaddr[4],
		addr.Multiaddr[5],
		addr.Multiaddr[6],
		addr.Multiaddr[7],
		uint16(addr.Multiaddr[2])<<8+uint16(addr.Multiaddr[3]))

	return rawaddr, host, nil
}
