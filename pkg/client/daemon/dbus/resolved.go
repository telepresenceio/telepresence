package dbus

import (
	"errors"
	"fmt"
	"net"
	"strings"
	"syscall"

	"github.com/godbus/dbus/v5"
)

type (
	// A ResolveD is a `dbus.Conn` with methods to communicate with the ResolveD daemon.
	ResolveD struct {
		*dbus.Conn
	}

	// A resolvedLinkAddress is the type of the array members of the argument to the SetLinkDNS DBus call. It consists
	// of an address family (either AF_INET or AF_INET6), followed by a 4-byte or 16-byte array with the raw address data.
	resolvedLinkAddress struct {
		Dialect int32
		IP      net.IP
	}

	// A resolvedDomain is the type of the array members of the argument to the SetLinkDomains DBus call. It is a domain
	// name string and a parameter identifying whether to include that domain in the search path, or only to be used for
	// deciding which DNS server to route a given request to.
	resolvedDomain struct {
		Name        string
		RoutingOnly bool
	}
)

func NewResolveD() (*ResolveD, error) {
	conn, err := dbus.ConnectSystemBus()
	if err != nil {
		return nil, fmt.Errorf("failed to connect to system bus: %v", err)
	}
	return &ResolveD{conn}, nil
}

func (conn *ResolveD) IsRunning() bool {
	var names []string
	err := conn.BusObject().Call("org.freedesktop.DBus.ListNames", 0).Store(&names)
	if err != nil {
		return false
	}
	for _, name := range names {
		if name == "org.freedesktop.resolve1" {
			return true
		}
	}
	return false
}

func (conn *ResolveD) SetLinkDNS(networkIndex int, ips ...net.IP) error {
	addrs := make([]resolvedLinkAddress, len(ips))
	for i, ip := range ips {
		addr := &addrs[i]
		switch len(ip) {
		case 4:
			addr.Dialect = syscall.AF_INET
		case 16:
			addr.Dialect = syscall.AF_INET6
		default:
			return errors.New("illegal IP (not AF_INET or AF_INET6")
		}
		addr.IP = ip
	}
	return conn.Object("org.freedesktop.resolve1", "/org/freedesktop/resolve1").Call(
		"org.freedesktop.resolve1.Manager.SetLinkDNS", dbus.FlagNoReplyExpected, int32(networkIndex), addrs).Err
}

func (conn *ResolveD) SetLinkDomains(networkIndex int, domains ...string) error {
	dds := make([]resolvedDomain, 0, len(domains))
	for _, domain := range domains {
		if domain == "" {
			continue
		}
		routing := false
		if strings.HasPrefix(domain, "~") {
			domain = domain[1:]
			routing = true
		}
		dds = append(dds, resolvedDomain{Name: domain, RoutingOnly: routing})
	}
	return conn.Object("org.freedesktop.resolve1", "/org/freedesktop/resolve1").Call(
		"org.freedesktop.resolve1.Manager.SetLinkDomains", dbus.FlagNoReplyExpected, int32(networkIndex), dds).Err
}

func (conn *ResolveD) RevertLink(networkIndex int) error {
	return conn.Object("org.freedesktop.resolve1", "/org/freedesktop/resolve1").Call(
		"org.freedesktop.resolve1.Manager.RevertLink", dbus.FlagNoReplyExpected, int32(networkIndex)).Err
}
