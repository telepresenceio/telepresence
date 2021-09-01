//go:build linux
// +build linux

package dbus

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"

	"github.com/godbus/dbus/v5"
	"golang.org/x/sys/unix"

	"github.com/datawire/dlib/dlog"
)

type (
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

func withDBus(c context.Context, f func(*dbus.Conn) error) error {
	conn, err := dbus.ConnectSystemBus()
	if err != nil {
		err = fmt.Errorf("failed to connect to system bus: %w", err)
		dlog.Error(c, err)
		return err
	}
	defer conn.Close()
	return f(conn)
}

func IsResolveDRunning(c context.Context) bool {
	err := withDBus(c, func(conn *dbus.Conn) error {
		var names []string
		if err := conn.BusObject().CallWithContext(c, "org.freedesktop.DBus.ListNames", 0).Store(&names); err != nil {
			return err
		}
		for _, name := range names {
			if name == "org.freedesktop.resolve1" {
				return nil
			}
		}
		return errors.New("not found")
	})
	return err == nil
}

func SetLinkDNS(c context.Context, networkIndex int, ips ...net.IP) error {
	return withDBus(c, func(conn *dbus.Conn) error {
		addrs := make([]resolvedLinkAddress, len(ips))
		for i, ip := range ips {
			addr := &addrs[i]
			switch len(ip) {
			case 4:
				addr.Dialect = unix.AF_INET
			case 16:
				addr.Dialect = unix.AF_INET6
			default:
				return errors.New("illegal IP (not AF_INET or AF_INET6")
			}
			addr.IP = ip
		}
		return conn.Object("org.freedesktop.resolve1", "/org/freedesktop/resolve1").CallWithContext(
			c, "org.freedesktop.resolve1.Manager.SetLinkDNS", 0, int32(networkIndex), addrs).Err
	})
}

func SetLinkDomains(c context.Context, networkIndex int, domains ...string) error {
	return withDBus(c, func(conn *dbus.Conn) error {
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
		return conn.Object("org.freedesktop.resolve1", "/org/freedesktop/resolve1").CallWithContext(
			c, "org.freedesktop.resolve1.Manager.SetLinkDomains", 0, int32(networkIndex), dds).Err
	})
}

func RevertLink(c context.Context, networkIndex int) error {
	return withDBus(c, func(conn *dbus.Conn) error {
		return conn.Object("org.freedesktop.resolve1", "/org/freedesktop/resolve1").CallWithContext(
			c, "org.freedesktop.resolve1.Manager.RevertLink", 0, int32(networkIndex)).Err
	})
}
