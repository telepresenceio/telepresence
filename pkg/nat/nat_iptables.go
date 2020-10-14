// +build linux

package nat

import (
	"fmt"
	"net"
	"strings"
	"syscall"

	"github.com/datawire/ambassador/pkg/supervisor"
)

type Translator struct {
	commonTranslator
}

func (t *Translator) ipt(p *supervisor.Process, args ...string) {
	cmd := p.Command("iptables", append([]string{"-t", "nat"}, args...)...)
	err := cmd.Start()
	if err != nil {
		panic(err)
	}
	cmd.Wait()
}

func (t *Translator) Enable(p *supervisor.Process) {
	// XXX: -D only removes one copy of the rule, need to figure out how to remove all copies just in case
	t.ipt(p, "-D", "OUTPUT", "-j", t.Name)
	// we need to be in the PREROUTING chain in order to get traffic
	// from docker containers, not sure you would *always* want this,
	// but probably makes sense as a default
	t.ipt(p, "-D", "PREROUTING", "-j", t.Name)
	t.ipt(p, "-N", t.Name)
	t.ipt(p, "-F", t.Name)
	t.ipt(p, "-I", "OUTPUT", "1", "-j", t.Name)
	t.ipt(p, "-I", "PREROUTING", "1", "-j", t.Name)
	t.ipt(p, "-A", t.Name, "-j", "RETURN", "--dest", "127.0.0.1/32", "-p", "tcp")
}

func (t *Translator) Disable(p *supervisor.Process) {
	// XXX: -D only removes one copy of the rule, need to figure out how to remove all copies just in case
	t.ipt(p, "-D", "OUTPUT", "-j", t.Name)
	t.ipt(p, "-D", "PREROUTING", "-j", t.Name)
	t.ipt(p, "-F", t.Name)
	t.ipt(p, "-X", t.Name)
}

func (t *Translator) ForwardTCP(p *supervisor.Process, ip, port, toPort string) {
	t.forward(p, "tcp", ip, port, toPort)
}

func (t *Translator) ForwardUDP(p *supervisor.Process, ip, port, toPort string) {
	t.forward(p, "udp", ip, port, toPort)
}

func (t *Translator) forward(p *supervisor.Process, protocol, ip, port, toPort string) {
	t.clear(p, protocol, ip, port)
	args := []string{"-A", t.Name, "-j", "REDIRECT", "-p", protocol, "--dest", ip + "/32"}
	if port != "" {
		if strings.Contains(port, ",") {
			args = append(args, "-m", "multiport", "--dports", port)
		} else {
			args = append(args, "--dport", port)
		}
	}
	args = append(args, "--to-ports", toPort)
	t.ipt(p, args...)
	t.Mappings[Address{protocol, ip, port}] = toPort
}

func (t *Translator) ClearTCP(p *supervisor.Process, ip, port string) {
	t.clear(p, "tcp", ip, port)
}

func (t *Translator) ClearUDP(p *supervisor.Process, ip, port string) {
	t.clear(p, "udp", ip, port)
}

func (t *Translator) clear(p *supervisor.Process, protocol, ip, port string) {
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
		t.ipt(p, args...)
		delete(t.Mappings, Address{protocol, ip, port})
	}
}

const (
	SO_ORIGINAL_DST      = 80
	IP6T_SO_ORIGINAL_DST = 80
)

// get the original destination for the socket when redirect by linux iptables
// refer to https://raw.githubusercontent.com/missdeer/avege/master/src/inbound/redir/redir_iptables.go
//
func (t *Translator) GetOriginalDst(conn *net.TCPConn) (rawaddr []byte, host string, err error) {
	var addr *syscall.IPv6Mreq

	// Get original destination
	// this is the only syscall in the Golang libs that I can find that returns 16 bytes
	// Example result: &{Multiaddr:[2 0 31 144 206 190 36 45 0 0 0 0 0 0 0 0] Interface:0}
	// port starts at the 3rd byte and is 2 bytes long (31 144 = port 8080)
	// IPv6 version, didn't find a way to detect network family
	//addr, err := syscall.GetsockoptIPv6Mreq(int(fd), syscall.IPPROTO_IPV6, IP6T_SO_ORIGINAL_DST)
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
