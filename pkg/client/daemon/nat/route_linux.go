package nat

import (
	"context"
	"fmt"
	"net"
	"os/exec"
	"strconv"
	"strings"
	"syscall"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/pkg/client/logging"
)

type iptablesRouter struct {
	routingTableCommon
}

func newRouter(name string, _ net.IP) FirewallRouter {
	return &iptablesRouter{
		routingTableCommon: routingTableCommon{
			Name:     name,
			mappings: make(map[Destination]*Route),
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

func portsString(ports []int) string {
	w := strings.Builder{}
	for i, p := range ports {
		if i > 0 {
			w.WriteByte(',')
		}
		w.WriteString(strconv.Itoa(p))
	}
	return w.String()
}

// Flush is a no-op on a linux router since all rules are applied immediately
func (t *iptablesRouter) Flush(_ context.Context) error {
	return nil
}

func (t *iptablesRouter) Add(c context.Context, r *Route) (bool, error) {
	if previous, exists := t.mappings[r.Destination]; exists {
		if r.ToPort == previous.ToPort {
			// source already forwards to the correct port
			return false, nil
		}
		_, err := t.Clear(c, r)
		if err != nil {
			return false, err
		}
	}
	args := []string{"-A", t.Name, "-j", "REDIRECT", "-p", r.Proto(), "--dest", r.IP().String() + "/32"}
	ports := r.Ports()
	switch len(ports) {
	case 0:
	case 1:
		args = append(args, "--dport", strconv.Itoa(ports[0]))
	default:
		args = append(args, "-m", "multiport", "--dports", portsString(ports))
	}
	args = append(args, "--to-ports", strconv.Itoa(r.ToPort))
	if err := t.ipt(c, args...); err != nil {
		return false, err
	}
	t.mappings[r.Destination] = r
	return true, nil
}

func (t *iptablesRouter) Clear(c context.Context, r *Route) (bool, error) {
	if previous, exists := t.mappings[r.Destination]; exists {
		args := []string{"-D", t.Name, "-j", "REDIRECT", "-p", r.Proto(), "--dest", r.IP().String() + "/32"}
		ports := r.Ports()
		switch len(ports) {
		case 0:
		case 1:
			args = append(args, "--dport", strconv.Itoa(ports[0]))
		default:
			args = append(args, "-m", "multiport", "--dports", portsString(ports))
		}
		args = append(args, "--to-ports", strconv.Itoa(previous.ToPort))
		if err := t.ipt(c, args...); err != nil {
			return false, err
		}
		delete(t.mappings, r.Destination)
		return true, nil
	}
	return false, nil
}

const SO_ORIGINAL_DST = 80

// GetOriginalDst gets the original destination for the socket when redirect by linux iptables
// refer to https://raw.githubusercontent.com/missdeer/avege/master/src/inbound/redir/redir_iptables.go
//
func (t *iptablesRouter) GetOriginalDst(conn *net.TCPConn) (host string, err error) {
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
		return "", err
	}

	err = rawConn.Control(func(fd uintptr) {
		addr, err = syscall.GetsockoptIPv6Mreq(int(fd), syscall.IPPROTO_IP, SO_ORIGINAL_DST)
	})
	if err != nil {
		return "", err
	}

	host = fmt.Sprintf("%d.%d.%d.%d:%d",
		addr.Multiaddr[4],
		addr.Multiaddr[5],
		addr.Multiaddr[6],
		addr.Multiaddr[7],
		uint16(addr.Multiaddr[2])<<8+uint16(addr.Multiaddr[3]))

	return host, nil
}
