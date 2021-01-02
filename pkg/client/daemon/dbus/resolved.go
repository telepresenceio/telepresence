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
	DBusResolveD struct {
		*dbus.Conn
	}

	resolvedLinkAddress struct {
		Dialect int32
		IP      net.IP
	}

	resolvedDomain struct {
		Name    string
		Routing bool
	}
)

func NewDbusResolveD() (*DBusResolveD, error) {
	conn, err := dbus.ConnectSystemBus()
	if err != nil {
		return nil, fmt.Errorf("failed to connect to system bus: %v", err)
	}
	return &DBusResolveD{conn}, nil
}

func (conn DBusResolveD) IsRunning() bool {
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

func (conn DBusResolveD) SetLinkDNS(networkIndex int, ips ...net.IP) error {
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

func (conn DBusResolveD) SetLinkDomains(networkIndex int, domains ...string) error {
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
		dds = append(dds, resolvedDomain{Name: domain, Routing: routing})
	}
	return conn.Object("org.freedesktop.resolve1", "/org/freedesktop/resolve1").Call(
		"org.freedesktop.resolve1.Manager.SetLinkDomains", dbus.FlagNoReplyExpected, int32(networkIndex), dds).Err
}

func (conn DBusResolveD) RevertLink(networkIndex int) error {
	return conn.Object("org.freedesktop.resolve1", "/org/freedesktop/resolve1").Call(
		"org.freedesktop.resolve1.Manager.RevertLink", dbus.FlagNoReplyExpected, int32(networkIndex)).Err
}
